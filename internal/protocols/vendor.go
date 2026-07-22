package protocols

import (
	"encoding/json"
	"math/rand"
	"sync"
)

// IRUpstreamBuf is a thread-safe upstream response buffer. It shares the
// maxStreamBufSize cap defined in relay.go.
type IRUpstreamBuf struct {
	mu  sync.Mutex
	buf []byte
}

func (b *IRUpstreamBuf) Append(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buf) < maxStreamBufSize {
		remain := maxStreamBufSize - len(b.buf)
		if len(data) > remain {
			data = data[:remain]
		}
		b.buf = append(b.buf, data...)
	}
}

func (b *IRUpstreamBuf) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf
}

func NewVendorBag() IRVendorBag {
	return make(IRVendorBag)
}

func (b IRVendorBag) Set(key string, value interface{}) {
	data, err := json.Marshal(value)
	if err == nil {
		b[key] = data
	}
}

func (b IRVendorBag) Get(key string) (json.RawMessage, bool) {
	v, ok := b[key]
	return v, ok
}

const idChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// RandomString returns a random ID string of length n using the alphanumeric alphabet.
func RandomString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = idChars[rand.Intn(len(idChars))]
	}
	return string(b)
}
