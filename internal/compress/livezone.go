package compress

import "encoding/json"

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
	p := &jsonParser{data: body}
	if !p.seekTopLevelArray("input") {
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
		cp := &jsonParser{data: body, i: items[i].start}
		blocks = append(blocks, cp.walkResponsesItem()...)
	}
	return blocks
}

// locateLiveZoneWithArrayKey is the generic form of locateLiveZone; arrayKey
// names the top-level array to scan ("messages" for Claude/chat).
func locateLiveZoneWithArrayKey(body []byte, mode int, arrayKey string) []LiveBlock {
	p := &jsonParser{data: body}
	if !p.seekTopLevelArray(arrayKey) {
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
		cp := &jsonParser{data: body, i: msgs[i].start}
		blocks = append(blocks, cp.walkMessage(mode)...)
	}
	return blocks
}

// msgSpan records the byte range and role of one message object.
type msgSpan struct {
	start, end int
	role       string
}

// jsonParser is a lightweight offset-aware JSON scanner. The field i is the
// current absolute offset; every span it returns is relative to data.
// It is lenient (it does not validate syntax); callers gate with json.Valid
// beforehand when they need strict legality.
type jsonParser struct {
	data []byte
	i    int
}

func (p *jsonParser) skipWS() {
	for p.i < len(p.data) {
		switch p.data[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

// parseString reads a JSON string starting at data[p.i] (which must be the
// opening quote). It advances p.i past the closing quote and returns the
// decoded content plus the (start, end) offsets such that
// data[start:end] is the quoted literal.
func (p *jsonParser) parseString() (string, int, int) {
	start := p.i
	j := p.i + 1
	for j < len(p.data) {
		c := p.data[j]
		if c == '\\' {
			j += 2
			continue
		}
		if c == '"' {
			j++
			break
		}
		j++
	}
	end := j
	var decoded string
	if err := json.Unmarshal(p.data[start:end], &decoded); err != nil {
		// Fallback: strip the surrounding quotes and take the raw bytes.
		// json.Valid is expected to have passed upstream, so this is defensive.
		if end-start >= 2 {
			decoded = string(p.data[start+1 : end-1])
		}
	}
	p.i = end
	return decoded, start, end
}

// skipValue advances p.i past one JSON value (string, object, array, number,
// boolean, or null).
func (p *jsonParser) skipValue() {
	p.skipWS()
	if p.i >= len(p.data) {
		return
	}
	switch p.data[p.i] {
	case '"':
		p.parseString()
	case '{':
		p.skipContainer('{', '}')
	case '[':
		p.skipContainer('[', ']')
	default:
		for p.i < len(p.data) {
			switch p.data[p.i] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return
			}
			p.i++
		}
	}
}

// skipContainer advances p.i past a matching open/close container (p.i must
// point at open). Bytes inside string literals do not participate in pairing.
func (p *jsonParser) skipContainer(open, close byte) {
	depth := 0
	for p.i < len(p.data) {
		c := p.data[p.i]
		switch c {
		case '"':
			p.parseString()
		case open:
			depth++
			p.i++
		case close:
			depth--
			p.i++
			if depth == 0 {
				return
			}
		default:
			p.i++
		}
	}
}

// seekTopLevelArray looks up key in the top-level object and, on hit, leaves
// p.i on the '[' that opens its array value. Returns true on hit.
func (p *jsonParser) seekTopLevelArray(key string) bool {
	p.skipWS()
	if p.i >= len(p.data) || p.data[p.i] != '{' {
		return false
	}
	p.i++
	for {
		p.skipWS()
		if p.i >= len(p.data) {
			return false
		}
		if p.data[p.i] == '}' {
			return false
		}
		if p.data[p.i] != '"' {
			p.i++
			continue
		}
		k, _, _ := p.parseString()
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ':' {
			p.i++
		}
		p.skipWS()
		if k == key {
			return p.i < len(p.data) && p.data[p.i] == '['
		}
		p.skipValue()
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ',' {
			p.i++
		}
	}
}

// collectMessages walks the messages array (p.i must point at '[') and
// returns the byte span and role of each message object.
func (p *jsonParser) collectMessages() []msgSpan {
	var msgs []msgSpan
	p.i++ // consume '['
	for {
		p.skipWS()
		if p.i >= len(p.data) {
			return msgs
		}
		if p.data[p.i] == ']' {
			p.i++
			return msgs
		}
		if p.data[p.i] == '{' {
			start := p.i
			role := p.parseObjectRole()
			msgs = append(msgs, msgSpan{start: start, end: p.i, role: role})
		} else {
			p.skipValue()
		}
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ',' {
			p.i++
		}
	}
}

// parseObjectRole parses one object (p.i must point at '{') and returns the
// value of its "role" field, advancing p.i past the closing '}'.
func (p *jsonParser) parseObjectRole() string {
	p.i++ // consume '{'
	var role string
	for {
		p.skipWS()
		if p.i >= len(p.data) {
			return role
		}
		if p.data[p.i] == '}' {
			p.i++
			return role
		}
		if p.data[p.i] != '"' {
			p.i++
			continue
		}
		key, _, _ := p.parseString()
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ':' {
			p.i++
		}
		p.skipWS()
		if key == "role" {
			if p.i < len(p.data) && p.data[p.i] == '"' {
				role, _, _ = p.parseString()
			} else {
				p.skipValue()
				role = "\x00" // role is present but not a string (e.g. null) — do not treat as user
			}
		} else {
			p.skipValue()
		}
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ',' {
			p.i++
		}
	}
}

// walkObjectForKey scans a JSON object (p.i must point at '{'); when the
// target key is encountered it delegates to handler to collect blocks, all
// other values are skipped. Used to share structure between chat and Gemini
// message walkers that differ only in target key.
func (p *jsonParser) walkObjectForKey(targetKey string, handler func() []LiveBlock) []LiveBlock {
	var out []LiveBlock
	p.skipWS()
	if p.i >= len(p.data) || p.data[p.i] != '{' {
		return out
	}
	p.i++ // consume '{'
	for {
		p.skipWS()
		if p.i >= len(p.data) {
			return out
		}
		if p.data[p.i] == '}' {
			p.i++
			return out
		}
		if p.data[p.i] != '"' {
			p.i++
			continue
		}
		key, _, _ := p.parseString()
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ':' {
			p.i++
		}
		p.skipWS()
		if key == targetKey {
			out = append(out, handler()...)
		} else {
			p.skipValue()
		}
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ',' {
			p.i++
		}
	}
}

// walkMessage parses one message object (p.i must point at '{') and collects
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
	p.skipWS()
	if p.i >= len(p.data) {
		return nil
	}
	switch p.data[p.i] {
	case '"':
		text, start, end := p.parseString()
		if mode == modeChat || mode == modeResponses {
			return []LiveBlock{{Range: [2]int{start, end}, Text: text}}
		}
		return nil
	case '[':
		return p.parseContentArray(mode)
	default:
		p.skipValue()
		return nil
	}
}

// parseContentArray walks the content array (p.i must point at '[') and
// collects blocks from each object element according to mode.
func (p *jsonParser) parseContentArray(mode int) []LiveBlock {
	var out []LiveBlock
	p.i++ // consume '['
	for {
		p.skipWS()
		if p.i >= len(p.data) {
			return out
		}
		if p.data[p.i] == ']' {
			p.i++
			return out
		}
		if p.data[p.i] == '{' {
			out = append(out, p.parseContentElement(mode)...)
		} else {
			p.skipValue()
		}
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ',' {
			p.i++
		}
	}
}

// parseContentElement parses one object element of the content array
// (p.i must point at '{'). Field order does not matter: type / text / content
// candidates are buffered first, and the output is selected after the object
// closes based on (mode, type).
func (p *jsonParser) parseContentElement(mode int) []LiveBlock {
	p.i++ // consume '{'
	var typ string
	var textBlock *LiveBlock     // "text" value of a type=text element
	var trContentStr *LiveBlock  // string-form content of a tool_result
	var trContentArr []LiveBlock // type=text blocks inside tool_result.content[]
	for {
		p.skipWS()
		if p.i >= len(p.data) {
			break
		}
		if p.data[p.i] == '}' {
			p.i++
			break
		}
		if p.data[p.i] != '"' {
			p.i++
			continue
		}
		key, _, _ := p.parseString()
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ':' {
			p.i++
		}
		p.skipWS()
		switch key {
		case "type":
			if p.i < len(p.data) && p.data[p.i] == '"' {
				typ, _, _ = p.parseString()
			} else {
				p.skipValue()
			}
		case "text":
			if p.i < len(p.data) && p.data[p.i] == '"' {
				t, s, e := p.parseString()
				textBlock = &LiveBlock{Range: [2]int{s, e}, Text: t}
			} else {
				p.skipValue()
			}
		case "content":
			switch {
			case p.i < len(p.data) && p.data[p.i] == '"':
				t, s, e := p.parseString()
				trContentStr = &LiveBlock{Range: [2]int{s, e}, Text: t}
			case p.i < len(p.data) && p.data[p.i] == '[':
				trContentArr = p.parseTextArray()
			default:
				p.skipValue()
			}
		default:
			p.skipValue()
		}
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ',' {
			p.i++
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

// parseTextArray walks the tool_result.content array (p.i must point at '[')
// and collects the text values of its type=text elements.
func (p *jsonParser) parseTextArray() []LiveBlock {
	var out []LiveBlock
	p.i++ // consume '['
	for {
		p.skipWS()
		if p.i >= len(p.data) {
			return out
		}
		if p.data[p.i] == ']' {
			p.i++
			return out
		}
		if p.data[p.i] == '{' {
			if blk := p.parseTextElement(); blk != nil {
				out = append(out, *blk)
			}
		} else {
			p.skipValue()
		}
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ',' {
			p.i++
		}
	}
}

// parseTextElement parses a {"type":"text","text":"..."} element (p.i must
// point at '{'). Field order does not matter. The text range is returned
// only when type=="text", otherwise nil.
func (p *jsonParser) parseTextElement() *LiveBlock {
	p.i++ // consume '{'
	var typ string
	var textBlock *LiveBlock
	for {
		p.skipWS()
		if p.i >= len(p.data) {
			break
		}
		if p.data[p.i] == '}' {
			p.i++
			break
		}
		if p.data[p.i] != '"' {
			p.i++
			continue
		}
		key, _, _ := p.parseString()
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ':' {
			p.i++
		}
		p.skipWS()
		switch key {
		case "type":
			if p.i < len(p.data) && p.data[p.i] == '"' {
				typ, _, _ = p.parseString()
			} else {
				p.skipValue()
			}
		case "text":
			if p.i < len(p.data) && p.data[p.i] == '"' {
				t, s, e := p.parseString()
				textBlock = &LiveBlock{Range: [2]int{s, e}, Text: t}
			} else {
				p.skipValue()
			}
		default:
			p.skipValue()
		}
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ',' {
			p.i++
		}
	}
	if typ == "text" {
		return textBlock
	}
	return nil
}

// --- OpenAI Responses API (/v1/responses) parsers ---

// collectResponsesItems walks the input array (p.i must point at '[') and
// returns the byte span and role of each item. It extends collectMessages
// by also recognizing items with type:"function_call_output" (which have no
// role field), reporting them with the synthetic role "function_call_output".
func (p *jsonParser) collectResponsesItems() []msgSpan {
	var items []msgSpan
	p.i++ // consume '['
	for {
		p.skipWS()
		if p.i >= len(p.data) {
			return items
		}
		if p.data[p.i] == ']' {
			p.i++
			return items
		}
		if p.data[p.i] == '{' {
			start := p.i
			role := p.parseObjectRoleResponses()
			items = append(items, msgSpan{start: start, end: p.i, role: role})
		} else {
			p.skipValue()
		}
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ',' {
			p.i++
		}
	}
}

// parseObjectRoleResponses parses one object (p.i must point at '{') while
// looking at both the "role" and "type" fields:
//   - "role" present  -> its value ("user" / "assistant" / "tool")
//   - "type":"function_call_output" -> "function_call_output"
//   - otherwise the empty string
func (p *jsonParser) parseObjectRoleResponses() string {
	p.i++ // consume '{'
	var role, typ string
	for {
		p.skipWS()
		if p.i >= len(p.data) {
			break
		}
		if p.data[p.i] == '}' {
			p.i++
			break
		}
		if p.data[p.i] != '"' {
			p.i++
			continue
		}
		key, _, _ := p.parseString()
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ':' {
			p.i++
		}
		p.skipWS()
		if key == "role" && p.i < len(p.data) && p.data[p.i] == '"' {
			role, _, _ = p.parseString()
		} else if key == "type" && p.i < len(p.data) && p.data[p.i] == '"' {
			typ, _, _ = p.parseString()
		} else {
			p.skipValue()
		}
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ',' {
			p.i++
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
	p := &jsonParser{data: body}
	if !p.seekTopLevelArray("contents") {
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
		cp := &jsonParser{data: body, i: msgs[i].start}
		blocks = append(blocks, cp.walkGeminiMessage()...)
	}
	return blocks
}

// walkGeminiMessage collects parts[].text blocks from a Gemini contents
// entry (p.i must point at '{').
func (p *jsonParser) walkGeminiMessage() []LiveBlock {
	return p.walkObjectForKey("parts", func() []LiveBlock {
		return p.walkGeminiParts()
	})
}

// walkGeminiParts walks the parts array (p.i must point at '[') and collects
// the "text" field of each part. functionResponse.response is an object and
// is skipped via skipValue.
func (p *jsonParser) walkGeminiParts() []LiveBlock {
	var out []LiveBlock
	if p.i >= len(p.data) || p.data[p.i] != '[' {
		p.skipValue()
		return out
	}
	p.i++ // consume '['
	for {
		p.skipWS()
		if p.i >= len(p.data) {
			return out
		}
		if p.data[p.i] == ']' {
			p.i++
			return out
		}
		if p.data[p.i] == '{' {
			out = append(out, p.walkGeminiPart()...)
		} else {
			p.skipValue()
		}
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ',' {
			p.i++
		}
	}
}

// walkGeminiPart extracts the "text" field from a single part object
// (p.i must point at '{'). Parts with thought:true (Gemini 2.0+ reasoning
// traces) are excluded: all keys are visited first, then the decision is
// applied based on the buffered thought flag.
func (p *jsonParser) walkGeminiPart() []LiveBlock {
	p.skipWS()
	if p.i >= len(p.data) || p.data[p.i] != '{' {
		return nil
	}
	p.i++ // consume '{'
	var thought bool
	var blk *LiveBlock
	for {
		p.skipWS()
		if p.i >= len(p.data) {
			break
		}
		if p.data[p.i] == '}' {
			p.i++
			break
		}
		if p.data[p.i] != '"' {
			p.i++
			continue
		}
		key, _, _ := p.parseString()
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ':' {
			p.i++
		}
		p.skipWS()
		switch key {
		case "thought":
			if p.i+4 <= len(p.data) && string(p.data[p.i:p.i+4]) == "true" {
				thought = true
				p.i += 4
			} else {
				p.skipValue()
			}
		case "text":
			if p.i < len(p.data) && p.data[p.i] == '"' {
				text, start, end := p.parseString()
				b := LiveBlock{Range: [2]int{start, end}, Text: text}
				blk = &b
			} else {
				p.skipValue()
			}
		default:
			p.skipValue()
		}
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ',' {
			p.i++
		}
	}
	if thought || blk == nil {
		return nil
	}
	return []LiveBlock{*blk}
}

// walkResponsesItem collects compressible blocks from one Responses API
// input item (p.i must point at '{'):
//   - role:user/tool items: blocks are taken from the "content" field
//     (modeResponses; input_text / output_text / text block types).
//   - type:function_call_output items: the "output" field is a plain string
//     and is collected directly as one LiveBlock.
func (p *jsonParser) walkResponsesItem() []LiveBlock {
	var out []LiveBlock
	p.skipWS()
	if p.i >= len(p.data) || p.data[p.i] != '{' {
		return out
	}
	p.i++ // consume '{'
	for {
		p.skipWS()
		if p.i >= len(p.data) {
			return out
		}
		if p.data[p.i] == '}' {
			p.i++
			return out
		}
		if p.data[p.i] != '"' {
			p.i++
			continue
		}
		key, _, _ := p.parseString()
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ':' {
			p.i++
		}
		p.skipWS()
		switch key {
		case "content":
			out = append(out, p.parseContent(modeResponses)...)
		case "output":
			if p.i < len(p.data) && p.data[p.i] == '"' {
				text, start, end := p.parseString()
				out = append(out, LiveBlock{Range: [2]int{start, end}, Text: text})
			} else {
				p.skipValue()
			}
		default:
			p.skipValue()
		}
		p.skipWS()
		if p.i < len(p.data) && p.data[p.i] == ',' {
			p.i++
		}
	}
}
