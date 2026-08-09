package compress

import "github.com/yolorouter/yolorouter/internal/compress/jsonspan"

// LiveBlock is one text field eligible for compression. Range is the
// half-open byte interval [start, end) of the QUOTED JSON string literal in
// the original body, so originalBody[Range[0]:Range[1]] is exactly the
// quoted literal (including the opening and closing quotes). Text is the
// decoded content of that literal.
type LiveBlock struct {
	Range [2]int
	Text  string
}

// Entry modes that determine which content fields are collectable for each
// protocol shape (Claude messages, OpenAI chat, OpenAI Responses).
const (
	modeClaude = iota
	modeChat
	modeResponses
)

// locateClaudeLiveZone finds compressible tool_result text blocks inside the
// latest user message of an Anthropic /v1/messages request. The frozen
// boundary is the last assistant message; any user message after it is in
// the live zone. An offset-aware scanner records exact byte ranges instead
// of reverse-searching for the decoded text value. Returns nil on parse
// failure or when no messages are present; callers map that to
// SkipReasonParseError / SkipReasonNoLiveZone.
func locateClaudeLiveZone(body []byte) []LiveBlock {
	return locateLiveZone(body, modeClaude)
}

// locateChatLiveZone finds compressible text inside OpenAI chat messages
// whose role is tool or user and that follow the last assistant message.
// Collected fields are messages[].content (when it is a string) and the
// .text of type=text content blocks.
func locateChatLiveZone(body []byte) []LiveBlock {
	return locateLiveZone(body, modeChat)
}

// locateLiveZone is the shared entry point for Claude and chat shapes:
//  1. A single offset-aware scan locates the messages array and records the
//     byte span and role of each message.
//  2. The frozen boundary (index of the last assistant message) is computed.
//  3. For each in-scope message after the boundary the content is re-parsed
//     starting from the message's recorded byte offset, producing LiveBlocks
//     with absolute ranges.
//
// All ranges are derived from absolute offsets; the scanner never backtracks
// on decoded text values, so messages with identical content cannot be
// mislocated.
func locateLiveZone(body []byte, mode int) []LiveBlock {
	return locateLiveZoneWithArrayKey(body, mode, "messages")
}

// locateResponsesLiveZone finds compressible text in an OpenAI Responses
// /v1/responses request. The frozen boundary is the last item whose role is
// "assistant"; subsequent user / tool / function_call_output items are in
// the live zone. function_call_output items expose their text via the
// "output" field rather than "content" and need dedicated handling.
func locateResponsesLiveZone(body []byte) []LiveBlock {
	p := &jsonParser{Scanner: jsonspan.Scanner{Data: body}}
	if !p.SeekTopLevelArray("input") {
		return nil
	}
	items := p.collectResponsesItems()
	if len(items) == 0 {
		return nil
	}
	lastAssistant := -1
	for i, m := range items {
		if m.role == "assistant" {
			lastAssistant = i
		}
	}
	var blocks []LiveBlock
	for i := lastAssistant + 1; i < len(items); i++ {
		role := items[i].role
		if role != "user" && role != "tool" && role != "function_call_output" {
			continue
		}
		cp := &jsonParser{Scanner: jsonspan.Scanner{Data: body, Pos: items[i].start}}
		blocks = append(blocks, cp.walkResponsesItem()...)
	}
	return blocks
}

// locateLiveZoneWithArrayKey is the generic form of locateLiveZone; arrayKey
// names the top-level array to scan ("messages" for Claude/chat).
func locateLiveZoneWithArrayKey(body []byte, mode int, arrayKey string) []LiveBlock {
	p := &jsonParser{Scanner: jsonspan.Scanner{Data: body}}
	if !p.SeekTopLevelArray(arrayKey) {
		return nil
	}
	msgs := p.collectMessages()
	if len(msgs) == 0 {
		return nil
	}
	lastAssistant := -1
	for i, m := range msgs {
		if m.role == "assistant" {
			lastAssistant = i
		}
	}
	var blocks []LiveBlock
	for i := lastAssistant + 1; i < len(msgs); i++ {
		role := msgs[i].role
		if mode == modeClaude {
			if role != "user" {
				continue
			}
		} else {
			if role != "tool" && role != "user" {
				continue
			}
		}
		cp := &jsonParser{Scanner: jsonspan.Scanner{Data: body, Pos: msgs[i].start}}
		blocks = append(blocks, cp.walkMessage(mode)...)
	}
	return blocks
}

// msgSpan records the byte range and role of one message object.
type msgSpan struct {
	start, end int
	role       string
}

// jsonParser layers the protocol-aware live-zone walkers over the generic
// offset-preserving scanner. The scanner (jsonspan.Scanner) knows JSON; the
// methods declared on this type know what a chat/claude/responses/gemini
// request body looks like inside.
type jsonParser struct {
	jsonspan.Scanner
}

// walkObjectForKey adapts the scanner's visit-callback walk to the walkers'
// block-collecting shape.
func (p *jsonParser) walkObjectForKey(targetKey string, handler func() []LiveBlock) []LiveBlock {
	var out []LiveBlock
	p.WalkObjectForKey(targetKey, func() { out = append(out, handler()...) })
	return out
}

// collectMessages walks the messages array (p.Pos must point at '[') and
// returns the byte span and role of each message object.
func (p *jsonParser) collectMessages() []msgSpan {
	var msgs []msgSpan
	p.Pos++ // consume '['
	for {
		p.SkipWS()
		if p.Pos >= len(p.Data) {
			return msgs
		}
		if p.Data[p.Pos] == ']' {
			p.Pos++
			return msgs
		}
		if p.Data[p.Pos] == '{' {
			start := p.Pos
			role := p.parseObjectRole()
			msgs = append(msgs, msgSpan{start: start, end: p.Pos, role: role})
		} else {
			p.SkipValue()
		}
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ',' {
			p.Pos++
		}
	}
}

// parseObjectRole parses one object (p.Pos must point at '{') and returns the
// value of its "role" field, advancing p.Pos past the closing '}'.
func (p *jsonParser) parseObjectRole() string {
	p.Pos++ // consume '{'
	var role string
	for {
		p.SkipWS()
		if p.Pos >= len(p.Data) {
			return role
		}
		if p.Data[p.Pos] == '}' {
			p.Pos++
			return role
		}
		if p.Data[p.Pos] != '"' {
			p.Pos++
			continue
		}
		key, _, _ := p.ParseString()
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ':' {
			p.Pos++
		}
		p.SkipWS()
		if key == "role" {
			if p.Pos < len(p.Data) && p.Data[p.Pos] == '"' {
				role, _, _ = p.ParseString()
			} else {
				p.SkipValue()
				role = "\x00" // role is present but not a string (e.g. null) — do not treat as user
			}
		} else {
			p.SkipValue()
		}
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ',' {
			p.Pos++
		}
	}
}

// walkMessage parses one message object (p.Pos must point at '{') and collects
// compressible blocks from its "content" field according to mode.
func (p *jsonParser) walkMessage(mode int) []LiveBlock {
	return p.walkObjectForKey("content", func() []LiveBlock {
		return p.parseContent(mode)
	})
}

// parseContent handles the value of a message's "content" field:
//   - chat / responses: a bare string is collected directly; inside an
//     array, the .text of type=text blocks is collected.
//   - claude: a bare string (plain user input) is not compressed; inside an
//     array, type=tool_result blocks are collected per the tool_result rules.
func (p *jsonParser) parseContent(mode int) []LiveBlock {
	p.SkipWS()
	if p.Pos >= len(p.Data) {
		return nil
	}
	switch p.Data[p.Pos] {
	case '"':
		text, start, end := p.ParseString()
		if mode == modeChat || mode == modeResponses {
			return []LiveBlock{{Range: [2]int{start, end}, Text: text}}
		}
		return nil
	case '[':
		return p.parseContentArray(mode)
	default:
		p.SkipValue()
		return nil
	}
}

// parseContentArray walks the content array (p.Pos must point at '[') and
// collects blocks from each object element according to mode.
func (p *jsonParser) parseContentArray(mode int) []LiveBlock {
	var out []LiveBlock
	p.Pos++ // consume '['
	for {
		p.SkipWS()
		if p.Pos >= len(p.Data) {
			return out
		}
		if p.Data[p.Pos] == ']' {
			p.Pos++
			return out
		}
		if p.Data[p.Pos] == '{' {
			out = append(out, p.parseContentElement(mode)...)
		} else {
			p.SkipValue()
		}
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ',' {
			p.Pos++
		}
	}
}

// parseContentElement parses one object element of the content array
// (p.Pos must point at '{'). Field order does not matter: type / text / content
// candidates are buffered first, and the output is selected after the object
// closes based on (mode, type).
func (p *jsonParser) parseContentElement(mode int) []LiveBlock {
	p.Pos++ // consume '{'
	var typ string
	var textBlock *LiveBlock     // "text" value of a type=text element
	var trContentStr *LiveBlock  // string-form content of a tool_result
	var trContentArr []LiveBlock // type=text blocks inside tool_result.content[]
	for {
		p.SkipWS()
		if p.Pos >= len(p.Data) {
			break
		}
		if p.Data[p.Pos] == '}' {
			p.Pos++
			break
		}
		if p.Data[p.Pos] != '"' {
			p.Pos++
			continue
		}
		key, _, _ := p.ParseString()
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ':' {
			p.Pos++
		}
		p.SkipWS()
		switch key {
		case "type":
			if p.Pos < len(p.Data) && p.Data[p.Pos] == '"' {
				typ, _, _ = p.ParseString()
			} else {
				p.SkipValue()
			}
		case "text":
			if p.Pos < len(p.Data) && p.Data[p.Pos] == '"' {
				t, s, e := p.ParseString()
				textBlock = &LiveBlock{Range: [2]int{s, e}, Text: t}
			} else {
				p.SkipValue()
			}
		case "content":
			switch {
			case p.Pos < len(p.Data) && p.Data[p.Pos] == '"':
				t, s, e := p.ParseString()
				trContentStr = &LiveBlock{Range: [2]int{s, e}, Text: t}
			case p.Pos < len(p.Data) && p.Data[p.Pos] == '[':
				trContentArr = p.parseTextArray()
			default:
				p.SkipValue()
			}
		default:
			p.SkipValue()
		}
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ',' {
			p.Pos++
		}
	}

	if mode == modeChat {
		if typ == "text" && textBlock != nil {
			return []LiveBlock{*textBlock}
		}
		return nil
	}
	if mode == modeResponses {
		// Responses API content block types: input_text (user) and
		// output_text (assistant); "text" is also accepted for flexibility.
		if (typ == "input_text" || typ == "output_text" || typ == "text") && textBlock != nil {
			return []LiveBlock{*textBlock}
		}
		return nil
	}
	// claude
	if typ == "tool_result" {
		if trContentStr != nil {
			return []LiveBlock{*trContentStr}
		}
		return trContentArr
	}
	return nil
}

// parseTextArray walks the tool_result.content array (p.Pos must point at '[')
// and collects the text values of its type=text elements.
func (p *jsonParser) parseTextArray() []LiveBlock {
	var out []LiveBlock
	p.Pos++ // consume '['
	for {
		p.SkipWS()
		if p.Pos >= len(p.Data) {
			return out
		}
		if p.Data[p.Pos] == ']' {
			p.Pos++
			return out
		}
		if p.Data[p.Pos] == '{' {
			if blk := p.parseTextElement(); blk != nil {
				out = append(out, *blk)
			}
		} else {
			p.SkipValue()
		}
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ',' {
			p.Pos++
		}
	}
}

// parseTextElement parses a {"type":"text","text":"..."} element (p.Pos must
// point at '{'). Field order does not matter. The text range is returned
// only when type=="text", otherwise nil.
func (p *jsonParser) parseTextElement() *LiveBlock {
	p.Pos++ // consume '{'
	var typ string
	var textBlock *LiveBlock
	for {
		p.SkipWS()
		if p.Pos >= len(p.Data) {
			break
		}
		if p.Data[p.Pos] == '}' {
			p.Pos++
			break
		}
		if p.Data[p.Pos] != '"' {
			p.Pos++
			continue
		}
		key, _, _ := p.ParseString()
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ':' {
			p.Pos++
		}
		p.SkipWS()
		switch key {
		case "type":
			if p.Pos < len(p.Data) && p.Data[p.Pos] == '"' {
				typ, _, _ = p.ParseString()
			} else {
				p.SkipValue()
			}
		case "text":
			if p.Pos < len(p.Data) && p.Data[p.Pos] == '"' {
				t, s, e := p.ParseString()
				textBlock = &LiveBlock{Range: [2]int{s, e}, Text: t}
			} else {
				p.SkipValue()
			}
		default:
			p.SkipValue()
		}
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ',' {
			p.Pos++
		}
	}
	if typ == "text" {
		return textBlock
	}
	return nil
}

// --- OpenAI Responses API (/v1/responses) parsers ---

// collectResponsesItems walks the input array (p.Pos must point at '[') and
// returns the byte span and role of each item. It extends collectMessages
// by also recognizing items with type:"function_call_output" (which have no
// role field), reporting them with the synthetic role "function_call_output".
func (p *jsonParser) collectResponsesItems() []msgSpan {
	var items []msgSpan
	p.Pos++ // consume '['
	for {
		p.SkipWS()
		if p.Pos >= len(p.Data) {
			return items
		}
		if p.Data[p.Pos] == ']' {
			p.Pos++
			return items
		}
		if p.Data[p.Pos] == '{' {
			start := p.Pos
			role := p.parseObjectRoleResponses()
			items = append(items, msgSpan{start: start, end: p.Pos, role: role})
		} else {
			p.SkipValue()
		}
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ',' {
			p.Pos++
		}
	}
}

// parseObjectRoleResponses parses one object (p.Pos must point at '{') while
// looking at both the "role" and "type" fields:
//   - "role" present  -> its value ("user" / "assistant" / "tool")
//   - "type":"function_call_output" -> "function_call_output"
//   - otherwise the empty string
func (p *jsonParser) parseObjectRoleResponses() string {
	p.Pos++ // consume '{'
	var role, typ string
	for {
		p.SkipWS()
		if p.Pos >= len(p.Data) {
			break
		}
		if p.Data[p.Pos] == '}' {
			p.Pos++
			break
		}
		if p.Data[p.Pos] != '"' {
			p.Pos++
			continue
		}
		key, _, _ := p.ParseString()
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ':' {
			p.Pos++
		}
		p.SkipWS()
		if key == "role" && p.Pos < len(p.Data) && p.Data[p.Pos] == '"' {
			role, _, _ = p.ParseString()
		} else if key == "type" && p.Pos < len(p.Data) && p.Data[p.Pos] == '"' {
			typ, _, _ = p.ParseString()
		} else {
			p.SkipValue()
		}
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ',' {
			p.Pos++
		}
	}
	if typ == "function_call_output" {
		return "function_call_output"
	}
	return role
}

// locateGeminiLiveZone finds compressible text in the parts[].text of the
// latest role=user contents entries of a Gemini native request. The frozen
// boundary is the last role=model entry; functionResponse.response is a JSON
// object and is intentionally not processed. Gemini uses role="model" (not
// "assistant") and the field is "parts" (not "content"), hence the dedicated
// implementation.
func locateGeminiLiveZone(body []byte) []LiveBlock {
	p := &jsonParser{Scanner: jsonspan.Scanner{Data: body}}
	if !p.SeekTopLevelArray("contents") {
		return nil
	}
	msgs := p.collectMessages()
	if len(msgs) == 0 {
		return nil
	}
	lastModel := -1
	for i, m := range msgs {
		if m.role == "model" {
			lastModel = i
		}
	}
	var blocks []LiveBlock
	for i := lastModel + 1; i < len(msgs); i++ {
		// Gemini's Role field is marked omitempty, so an absent role is
		// treated as user (matching the IR decoder contract).
		if msgs[i].role != "user" && msgs[i].role != "" {
			continue
		}
		cp := &jsonParser{Scanner: jsonspan.Scanner{Data: body, Pos: msgs[i].start}}
		blocks = append(blocks, cp.walkGeminiMessage()...)
	}
	return blocks
}

// walkGeminiMessage collects parts[].text blocks from a Gemini contents
// entry (p.Pos must point at '{').
func (p *jsonParser) walkGeminiMessage() []LiveBlock {
	return p.walkObjectForKey("parts", func() []LiveBlock {
		return p.walkGeminiParts()
	})
}

// walkGeminiParts walks the parts array (p.Pos must point at '[') and collects
// the "text" field of each part. functionResponse.response is an object and
// is skipped via skipValue.
func (p *jsonParser) walkGeminiParts() []LiveBlock {
	var out []LiveBlock
	if p.Pos >= len(p.Data) || p.Data[p.Pos] != '[' {
		p.SkipValue()
		return out
	}
	p.Pos++ // consume '['
	for {
		p.SkipWS()
		if p.Pos >= len(p.Data) {
			return out
		}
		if p.Data[p.Pos] == ']' {
			p.Pos++
			return out
		}
		if p.Data[p.Pos] == '{' {
			out = append(out, p.walkGeminiPart()...)
		} else {
			p.SkipValue()
		}
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ',' {
			p.Pos++
		}
	}
}

// walkGeminiPart extracts the "text" field from a single part object
// (p.Pos must point at '{'). Parts with thought:true (Gemini 2.0+ reasoning
// traces) are excluded: all keys are visited first, then the decision is
// applied based on the buffered thought flag.
func (p *jsonParser) walkGeminiPart() []LiveBlock {
	p.SkipWS()
	if p.Pos >= len(p.Data) || p.Data[p.Pos] != '{' {
		return nil
	}
	p.Pos++ // consume '{'
	var thought bool
	var blk *LiveBlock
	for {
		p.SkipWS()
		if p.Pos >= len(p.Data) {
			break
		}
		if p.Data[p.Pos] == '}' {
			p.Pos++
			break
		}
		if p.Data[p.Pos] != '"' {
			p.Pos++
			continue
		}
		key, _, _ := p.ParseString()
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ':' {
			p.Pos++
		}
		p.SkipWS()
		switch key {
		case "thought":
			if p.Pos+4 <= len(p.Data) && string(p.Data[p.Pos:p.Pos+4]) == "true" {
				thought = true
				p.Pos += 4
			} else {
				p.SkipValue()
			}
		case "text":
			if p.Pos < len(p.Data) && p.Data[p.Pos] == '"' {
				text, start, end := p.ParseString()
				b := LiveBlock{Range: [2]int{start, end}, Text: text}
				blk = &b
			} else {
				p.SkipValue()
			}
		default:
			p.SkipValue()
		}
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ',' {
			p.Pos++
		}
	}
	if thought || blk == nil {
		return nil
	}
	return []LiveBlock{*blk}
}

// walkResponsesItem collects compressible blocks from one Responses API
// input item (p.Pos must point at '{'):
//   - role:user/tool items: blocks are taken from the "content" field
//     (modeResponses; input_text / output_text / text block types).
//   - type:function_call_output items: the "output" field is a plain string
//     and is collected directly as one LiveBlock.
func (p *jsonParser) walkResponsesItem() []LiveBlock {
	var out []LiveBlock
	p.SkipWS()
	if p.Pos >= len(p.Data) || p.Data[p.Pos] != '{' {
		return out
	}
	p.Pos++ // consume '{'
	for {
		p.SkipWS()
		if p.Pos >= len(p.Data) {
			return out
		}
		if p.Data[p.Pos] == '}' {
			p.Pos++
			return out
		}
		if p.Data[p.Pos] != '"' {
			p.Pos++
			continue
		}
		key, _, _ := p.ParseString()
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ':' {
			p.Pos++
		}
		p.SkipWS()
		switch key {
		case "content":
			out = append(out, p.parseContent(modeResponses)...)
		case "output":
			if p.Pos < len(p.Data) && p.Data[p.Pos] == '"' {
				text, start, end := p.ParseString()
				out = append(out, LiveBlock{Range: [2]int{start, end}, Text: text})
			} else {
				p.SkipValue()
			}
		default:
			p.SkipValue()
		}
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ',' {
			p.Pos++
		}
	}
}
