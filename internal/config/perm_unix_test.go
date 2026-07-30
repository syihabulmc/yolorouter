//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// statWithMode writes a file and forces mode onto it. os.WriteFile's mode
// argument is masked by the process umask, so the explicit Chmod is what
// actually pins the bits the check under test reads.
func statWithMode(t *testing.T, mode os.FileMode) (os.FileInfo, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 8080\n"), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %04o: %v", mode, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat temp config: %v", err)
	}
	return info, path
}

// The config file stores the master key that encrypts every upstream
// credential, so any mode readable by another local account must be refused.
func TestCheckConfigFilePermRejectsGroupOrOtherAccess(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o660, 0o666, 0o700 | 0o040, 0o777} {
		info, path := statWithMode(t, mode)
		err := checkConfigFilePerm(info, path)
		if err == nil {
			t.Errorf("mode %04o: expected rejection, got nil", mode)
			continue
		}
		// The message must stay actionable — it is the only instruction the
		// operator gets, and startup aborts on it.
		if !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("mode %04o: error should tell the operator how to fix it, got %v", mode, err)
		}
	}
}

func TestCheckConfigFilePermAcceptsOwnerOnlyModes(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o400, 0o700} {
		info, path := statWithMode(t, mode)
		if err := checkConfigFilePerm(info, path); err != nil {
			t.Errorf("mode %04o: expected acceptance, got %v", mode, err)
		}
	}
}

func TestPermEnforcementSupportedIsTrueOnUnix(t *testing.T) {
	if !PermEnforcementSupported {
		t.Fatal("PermEnforcementSupported must be true on unix, where the check is real")
	}
}
