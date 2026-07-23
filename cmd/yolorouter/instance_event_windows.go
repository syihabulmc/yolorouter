//go:build windows

package main

import (
	"path/filepath"
	"strings"

	"github.com/yolorouter/yolorouter/pkg/crypto"
)

// stopEventName returns the name of the manual-reset event used to ask a
// running server to shut down. serve creates and waits on it; stop opens and
// sets it. The name is derived from the resolved SQLite path so one deployment
// maps to exactly one event and separate deployments never collide. The Local
// namespace keeps it per-session and avoids the privileges Global requires.
//
// The path is canonicalized (cleaned + lowercased) before hashing. Windows
// paths are case-insensitive and tolerate separator/spelling variants that
// still open the same file, but a kernel object name is matched byte-for-byte,
// so serve and stop must fold those variants to one identity — otherwise a
// config path spelled with different casing on the two invocations would derive
// different event names for the same deployment, and stop could never reach the
// server's event.
func stopEventName(sqlitePath string) string {
	canonical := strings.ToLower(filepath.Clean(sqlitePath))
	// HashToken is hex(sha256(x)); the first 16 hex chars keep distinct
	// deployments' event names apart while staying well under the name limit.
	return `Local\yolorouter-stop-` + crypto.HashToken(canonical)[:16]
}
