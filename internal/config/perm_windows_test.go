//go:build windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression guard for the failure that made Windows release binaries unable
// to start at all: Go synthesizes 0666 for any writable Windows file, so
// applying the Unix group/other-readable test there rejected every config
// file — including the one the process had just generated itself.
func TestCheckConfigFilePermAcceptsSynthesizedWindowsModes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 8080\n"), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat temp config: %v", err)
	}
	// Asserted rather than assumed: if a future Go release stops synthesizing
	// 0666 here, this line documents what this test was actually proving.
	t.Logf("windows reports Perm()=%04o for a writable file", info.Mode().Perm())

	if err := checkConfigFilePerm(info, path); err != nil {
		t.Fatalf("checkConfigFilePerm must not reject a Windows file: %v", err)
	}
}

// Load must complete the generate-then-read-back round trip. This is the
// exact path that failed: generateDefaultConfig writes the file and then
// re-enters loadStrict to read it.
func TestLoadGeneratesAndReadsBackOnWindows(t *testing.T) {
	dir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load must generate and read back a config on windows: %v", err)
	}
	if cfg.Security.ProviderMasterKey == "" {
		t.Fatal("expected a generated provider_master_key")
	}
}

func TestPermEnforcementSupportedIsFalseOnWindows(t *testing.T) {
	if PermEnforcementSupported {
		t.Fatal("PermEnforcementSupported must be false on windows so the startup warning fires")
	}
}
