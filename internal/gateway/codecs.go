package gateway

import (
	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/protocols/chat"
	"github.com/yolorouter/yolorouter/internal/protocols/claude"
	"github.com/yolorouter/yolorouter/internal/protocols/gemini"
	"github.com/yolorouter/yolorouter/internal/protocols/responses"
)

// protocolCodecs groups the six codec constructors for one wire protocol, so
// adding a new protocol only ever touches codecRegistry. Request/Response
// codecs are stateless empty structs and are stored by value; the stream
// codecs are stateful per-request state machines and must be constructed
// fresh for every call, hence the New... factory function fields.
type protocolCodecs struct {
	RequestDecoder   protocols.RequestDecoder
	RequestEncoder   protocols.RequestEncoder
	ResponseDecoder  protocols.ResponseDecoder
	ResponseEncoder  protocols.ResponseEncoder
	NewStreamDecoder func() protocols.StreamDecoder
	NewStreamEncoder func() protocols.StreamEncoder
}

var codecRegistry = map[protocols.ProtocolID]protocolCodecs{
	protocols.ProtocolOpenAI: {
		RequestDecoder:   chat.RequestDecoder{},
		RequestEncoder:   chat.RequestEncoder{},
		ResponseDecoder:  chat.ResponseDecoder{},
		ResponseEncoder:  chat.ResponseEncoder{},
		NewStreamDecoder: func() protocols.StreamDecoder { return chat.NewStreamDecoder() },
		NewStreamEncoder: func() protocols.StreamEncoder { return chat.NewStreamEncoder() },
	},
	protocols.ProtocolClaude: {
		RequestDecoder:   claude.RequestDecoder{},
		RequestEncoder:   claude.RequestEncoder{},
		ResponseDecoder:  claude.ResponseDecoder{},
		ResponseEncoder:  claude.ResponseEncoder{},
		NewStreamDecoder: func() protocols.StreamDecoder { return claude.NewStreamDecoder() },
		NewStreamEncoder: func() protocols.StreamEncoder { return claude.NewStreamEncoder() },
	},
	protocols.ProtocolGemini: {
		RequestDecoder:   gemini.RequestDecoder{},
		RequestEncoder:   gemini.RequestEncoder{},
		ResponseDecoder:  gemini.ResponseDecoder{},
		ResponseEncoder:  gemini.ResponseEncoder{},
		NewStreamDecoder: func() protocols.StreamDecoder { return gemini.NewStreamDecoder() },
		NewStreamEncoder: func() protocols.StreamEncoder { return gemini.NewStreamEncoder() },
	},
	protocols.ProtocolResponses: {
		RequestDecoder:   responses.RequestDecoder{},
		RequestEncoder:   responses.RequestEncoder{},
		ResponseDecoder:  responses.ResponseDecoder{},
		ResponseEncoder:  responses.ResponseEncoder{},
		NewStreamDecoder: func() protocols.StreamDecoder { return responses.NewStreamDecoder() },
		NewStreamEncoder: func() protocols.StreamEncoder { return responses.NewStreamEncoder() },
	},
}

// codecsFor returns the codec group for a protocol; an unregistered protocol
// falls back to OpenAI Chat, mirroring the ingress/egress default elsewhere
// in the gateway.
func codecsFor(p protocols.ProtocolID) protocolCodecs {
	if c, ok := codecRegistry[p]; ok {
		return c
	}
	return codecRegistry[protocols.ProtocolOpenAI]
}
