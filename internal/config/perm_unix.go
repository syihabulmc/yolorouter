//go:build !windows

package config

import (
	"fmt"
	"os"
)

// PermEnforcementSupported reports whether this platform can enforce
// restrictive permissions on the config file. True on Unix, where the check
// in checkConfigFilePerm reads real permission bits.
const PermEnforcementSupported = true

// checkConfigFilePerm rejects a config file that is group- or other-readable.
// The file holds security.provider_master_key in plaintext, so a mode that
// lets any other local account read it defeats encrypting the stored upstream
// keys at all.
func checkConfigFilePerm(info os.FileInfo, path string) error {
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("config file %s must not be group- or other-readable (mode %04o); run chmod 600 %s", path, info.Mode().Perm(), path)
	}
	return nil
}
