//go:build windows

package config

import "os"

// PermEnforcementSupported reports whether this platform can enforce
// restrictive permissions on the config file. False on Windows: access is
// governed by ACLs, and the Unix permission bits Go reports for a Windows
// file are synthesized from a single attribute — 0666 for any writable file,
// 0444 for a read-only one (see os.fileStat.Mode in the standard library).
// They carry no information about who can actually read the file, so no
// meaningful check can be derived from them. os.Chmod cannot help either: on
// Windows it only toggles the read-only attribute, so Chmod(0600) still
// leaves a file reporting 0666.
//
// Callers that want to tell the operator about this gap read this constant
// rather than testing runtime.GOOS, so the reason lives in one place.
const PermEnforcementSupported = false

// checkConfigFilePerm is a no-op on Windows. Applying the Unix check here
// would reject every config file unconditionally, because the synthesized
// 0666 of a writable file always trips a group/other-readable test — which is
// exactly the failure that made release binaries unable to start at all.
func checkConfigFilePerm(_ os.FileInfo, _ string) error { return nil }
