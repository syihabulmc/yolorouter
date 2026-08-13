// Package loopback holds the process-level secret and header names for
// gateway self-calls (a capability calling back into this gateway's own API
// on behalf of a caller request). It is deliberately tiny and neutral: both
// the gateway kernel (which recognizes an incoming self-call) and the
// capability that makes one import it, and neither may import the other.
package loopback

import (
	"crypto/rand"
	"encoding/hex"
)

const (
	// HeaderInternal carries Token on a self-call; the kernel marks a request
	// bearing the correct value as gateway-initiated, and capabilities that
	// could recurse read that mark to decline. The name deliberately contains
	// "token" so the header-sanitization denylist masks its value before any
	// header capture is persisted.
	HeaderInternal = "X-Yolorouter-Loopback-Token"
	// HeaderParent names the caller request a self-call works for.
	HeaderParent = "X-Yolorouter-Loopback-Parent"
)

// Token is generated fresh at every process start and is only ever sent by
// this process to itself, so an outside caller cannot learn a usable value:
// yesterday's logs (where it is masked anyway) would hold yesterday's token.
var Token = mustGenerateToken()

func mustGenerateToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("loopback: generate internal token: " + err.Error())
	}
	return hex.EncodeToString(b)
}
