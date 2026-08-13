package visionfallback

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// This file locates and replaces image parts by surgical JSON patching:
// every layer is unmarshalled into map[string]json.RawMessage /
// []json.RawMessage, so NO FIELD is ever dropped — an untouched value's
// bytes survive up to Go's re-marshalling of the containers around it
// (object key order may change, and <, >, & in strings gain JSON escapes;
// both are semantically neutral). A whole-request decode→re-encode through
// the IR was rejected for this job — request encoders rebuild only the
// fields the IR models, which silently dropped everything else (sampling
// knobs, user tags, provider extras) from any image-bearing request.

// imageMarkers are the per-protocol byte substrings whose absence proves the
// body has no image content — the cheap gate that keeps the common case
// (text-only traffic) from ever paying for a JSON parse.
var imageMarkers = map[protocols.ProtocolID][]string{
	protocols.ProtocolOpenAI:    {"image_url", `"image"`},
	protocols.ProtocolClaude:    {`"image"`},
	protocols.ProtocolResponses: {"input_image"},
	protocols.ProtocolGemini:    {"inline_data", "inlineData", "file_data", "fileData"},
}

func hasImageMarker(proto protocols.ProtocolID, body []byte) bool {
	markers, ok := imageMarkers[proto]
	if !ok {
		return false
	}
	for _, m := range markers {
		if bytes.Contains(body, []byte(m)) {
			return true
		}
	}
	return false
}

// extractModelName pulls the public model name the caller asked for. Three
// protocols carry it in the body; Gemini carries it in the URL path
// (/v1beta/models/{model}:{action}) — parsed the same way the kernel's own
// Gemini ingress does: split on the LAST colon (model names may contain
// colons) and URL-unescape.
func extractModelName(proto protocols.ProtocolID, path string, body []byte) string {
	if proto == protocols.ProtocolGemini {
		return geminiModelFromPath(path)
	}
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}
	return probe.Model
}

func geminiModelFromPath(path string) string {
	const prefix = "/models/"
	i := strings.LastIndex(path, prefix)
	if i < 0 {
		return ""
	}
	rest := path[i+len(prefix):]
	if j := strings.LastIndexByte(rest, ':'); j >= 0 {
		rest = rest[:j]
	}
	if unescaped, err := url.PathUnescape(rest); err == nil {
		return unescaped
	}
	return rest
}

// imageSite is one located image part: the URL the describe call should
// fetch it by, and how big its inline payload is (0 for a real URL) for the
// size limit. Position is not stored — the replace walk repeats the same
// deterministic traversal, so index alignment is the linkage.
type imageSite struct {
	url        string
	payloadLen int
}

// protocolShape drives one walk for one protocol: which top-level array to
// traverse, which per-item field holds the parts, how to recognize an image
// part and extract its URL, and how to render a replacement text part.
type protocolShape struct {
	arrayField string
	partsField string
	// imagePart returns the describe URL + inline payload length when the
	// part is an image, ok=false otherwise.
	imagePart func(part map[string]json.RawMessage) (site imageSite, ok bool)
	textPart  func(text string) json.RawMessage
	// topLevelImage handles protocols whose top-level array items may BE an
	// image (Responses bare input_image items); nil elsewhere. wrap renders
	// the replacement for such an item.
	topLevelImage func(item map[string]json.RawMessage) (site imageSite, ok bool)
	topLevelWrap  func(text string) json.RawMessage
}

func jsonText(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func partType(part map[string]json.RawMessage) string {
	raw, ok := part["type"]
	if !ok {
		return ""
	}
	var t string
	_ = json.Unmarshal(raw, &t)
	return t
}

// chatImagePart recognizes both the OpenAI part shape
// ({"type":"image_url","image_url":{"url":...}}) and the Anthropic shape
// ({"type":"image","source":{...}}), which some OpenAI-compatible clients
// also emit on the chat endpoint.
func chatImagePart(part map[string]json.RawMessage) (imageSite, bool) {
	switch partType(part) {
	case "image_url":
		var f struct {
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if err := unmarshalFields(part, &f); err != nil || f.ImageURL.URL == "" {
			return imageSite{}, false
		}
		site := imageSite{url: f.ImageURL.URL}
		if strings.HasPrefix(f.ImageURL.URL, "data:") {
			site.payloadLen = len(f.ImageURL.URL)
		}
		return site, true
	case "image":
		return claudeSourceSite(part)
	}
	return imageSite{}, false
}

func claudeSourceSite(part map[string]json.RawMessage) (imageSite, bool) {
	var f struct {
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
			URL       string `json:"url"`
		} `json:"source"`
	}
	if err := unmarshalFields(part, &f); err != nil {
		return imageSite{}, false
	}
	switch f.Source.Type {
	case "url":
		if f.Source.URL == "" {
			return imageSite{}, false
		}
		return imageSite{url: f.Source.URL}, true
	case "base64":
		if f.Source.Data == "" {
			return imageSite{}, false
		}
		mt := f.Source.MediaType
		if mt == "" {
			mt = "image/png"
		}
		return imageSite{url: "data:" + mt + ";base64," + f.Source.Data, payloadLen: len(f.Source.Data)}, true
	}
	return imageSite{}, false
}

func responsesImagePart(part map[string]json.RawMessage) (imageSite, bool) {
	if partType(part) != "input_image" {
		return imageSite{}, false
	}
	// Responses carries image_url as a plain string.
	var f struct {
		ImageURL string `json:"image_url"`
	}
	if err := unmarshalFields(part, &f); err != nil || f.ImageURL == "" {
		return imageSite{}, false
	}
	site := imageSite{url: f.ImageURL}
	if strings.HasPrefix(f.ImageURL, "data:") {
		site.payloadLen = len(f.ImageURL)
	}
	return site, true
}

// geminiImagePart: Gemini parts have no "type" discriminator — image-ness is
// the presence of inline_data/inlineData (base64) or file_data/fileData (a
// file URI).
func geminiImagePart(part map[string]json.RawMessage) (imageSite, bool) {
	for _, key := range []string{"inline_data", "inlineData"} {
		raw, ok := part[key]
		if !ok {
			continue
		}
		var f struct {
			MimeType  string `json:"mime_type"`
			MimeType2 string `json:"mimeType"`
			Data      string `json:"data"`
		}
		if json.Unmarshal(raw, &f) != nil || f.Data == "" {
			continue // malformed inline entry; the part may still carry file data
		}
		mt := f.MimeType
		if mt == "" {
			mt = f.MimeType2
		}
		if mt == "" || !strings.HasPrefix(mt, "image/") {
			return imageSite{}, false
		}
		return imageSite{url: "data:" + mt + ";base64," + f.Data, payloadLen: len(f.Data)}, true
	}
	for _, key := range []string{"file_data", "fileData"} {
		raw, ok := part[key]
		if !ok {
			continue
		}
		var f struct {
			FileURI   string `json:"file_uri"`
			FileURI2  string `json:"fileUri"`
			MimeType  string `json:"mime_type"`
			MimeType2 string `json:"mimeType"`
		}
		if json.Unmarshal(raw, &f) != nil {
			return imageSite{}, false
		}
		uri := f.FileURI
		if uri == "" {
			uri = f.FileURI2
		}
		mt := f.MimeType
		if mt == "" {
			mt = f.MimeType2
		}
		// file_data carries any modality (audio, video, PDF); only a
		// declared image MIME is this capability's business — a video sent
		// to an images-blind model is not ours to describe or strip.
		if uri == "" || !strings.HasPrefix(mt, "image/") {
			return imageSite{}, false
		}
		return imageSite{url: uri}, true
	}
	return imageSite{}, false
}

// unmarshalFields re-marshals the part map and decodes the selected fields —
// small bodies, called only on parts already known to be candidates.
func unmarshalFields(part map[string]json.RawMessage, into any) error {
	b, err := json.Marshal(part)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

var shapes = map[protocols.ProtocolID]protocolShape{
	protocols.ProtocolOpenAI: {
		arrayField: "messages", partsField: "content",
		imagePart: chatImagePart,
		textPart:  func(t string) json.RawMessage { return json.RawMessage(`{"type":"text","text":` + jsonText(t) + `}`) },
	},
	protocols.ProtocolClaude: {
		arrayField: "messages", partsField: "content",
		imagePart: func(p map[string]json.RawMessage) (imageSite, bool) {
			if partType(p) != "image" {
				return imageSite{}, false
			}
			return claudeSourceSite(p)
		},
		textPart: func(t string) json.RawMessage { return json.RawMessage(`{"type":"text","text":` + jsonText(t) + `}`) },
	},
	protocols.ProtocolResponses: {
		arrayField: "input", partsField: "content",
		imagePart: responsesImagePart,
		textPart: func(t string) json.RawMessage {
			return json.RawMessage(`{"type":"input_text","text":` + jsonText(t) + `}`)
		},
		topLevelImage: func(item map[string]json.RawMessage) (imageSite, bool) {
			return responsesImagePart(item)
		},
		// A bare top-level input_image is replaced by a full message item so
		// downstream decoders recognize it instead of skipping a role-less
		// entry.
		topLevelWrap: func(t string) json.RawMessage {
			return json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":` + jsonText(t) + `}]}`)
		},
	},
	protocols.ProtocolGemini: {
		arrayField: "contents", partsField: "parts",
		imagePart: geminiImagePart,
		textPart:  func(t string) json.RawMessage { return json.RawMessage(`{"text":` + jsonText(t) + `}`) },
	},
}

// walkImages traverses body's message/part structure in one deterministic
// order. When replace is nil it only collects sites; when replace is non-nil,
// the i-th located image part is substituted with replace(i) and the patched
// body is returned. Collection and replacement share this single walker so
// their orders cannot disagree.
func walkImages(proto protocols.ProtocolID, body []byte, replace func(i int) json.RawMessage) (sites []imageSite, patched []byte, err error) {
	shape, ok := shapes[proto]
	if !ok {
		// Unreachable through RewriteIngress (hasImageMarker gates on the
		// same four keys) but load-bearing for direct callers: without it a
		// zero-value shape would walk with empty field names and nil part
		// renderers.
		return nil, body, nil
	}
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, body, err
	}
	arrayRaw, ok := req[shape.arrayField]
	if !ok {
		return nil, body, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(arrayRaw, &items); err != nil {
		// Responses "input" may be a plain string — no image parts then.
		return nil, body, nil
	}

	idx := 0
	modifiedAny := false
	for mi, itemRaw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(itemRaw, &item) != nil {
			continue
		}
		if shape.topLevelImage != nil {
			if site, ok := shape.topLevelImage(item); ok {
				sites = append(sites, site)
				if replace != nil {
					items[mi] = shape.topLevelWrap(textOf(replace(idx)))
					modifiedAny = true
				}
				idx++
				continue
			}
		}
		partsRaw, ok := item[shape.partsField]
		if !ok {
			continue
		}
		var parts []json.RawMessage
		if json.Unmarshal(partsRaw, &parts) != nil {
			continue // string content — cannot hold image parts
		}
		modified := false
		for pi, partRaw := range parts {
			var part map[string]json.RawMessage
			if json.Unmarshal(partRaw, &part) != nil {
				continue
			}
			site, ok := shape.imagePart(part)
			if !ok {
				continue
			}
			sites = append(sites, site)
			if replace != nil {
				parts[pi] = replace(idx)
				modified = true
			}
			idx++
		}
		if modified {
			newParts, err := json.Marshal(parts)
			if err != nil {
				return nil, body, err
			}
			item[shape.partsField] = newParts
			newItem, err := json.Marshal(item)
			if err != nil {
				return nil, body, err
			}
			items[mi] = newItem
			modifiedAny = true
		}
	}
	if replace == nil || !modifiedAny {
		return sites, body, nil
	}
	newItems, err := json.Marshal(items)
	if err != nil {
		return nil, body, err
	}
	req[shape.arrayField] = newItems
	out, err := json.Marshal(req)
	if err != nil {
		return nil, body, err
	}
	return sites, out, nil
}

// textOf extracts the text a replacement part carries, for the top-level
// wrap path (which renders a whole message item instead of a part).
func textOf(part json.RawMessage) string {
	var f struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(part, &f)
	return f.Text
}

func collectImageSites(proto protocols.ProtocolID, body []byte) ([]imageSite, error) {
	sites, _, err := walkImages(proto, body, nil)
	return sites, err
}

// replaceImageTexts substitutes the i-th located image part with texts[i],
// rendered in the protocol's own text-part shape.
func replaceImageTexts(proto protocols.ProtocolID, body []byte, texts []string) ([]byte, error) {
	shape := shapes[proto]
	_, out, err := walkImages(proto, body, func(i int) json.RawMessage {
		return shape.textPart(texts[i])
	})
	return out, err
}
