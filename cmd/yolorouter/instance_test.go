package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestInstancePIDPath(t *testing.T) {
	if got := instancePIDPath("/data/yolorouter.db"); got != "/data/yolorouter.db.pid" {
		t.Fatalf("instancePIDPath = %q", got)
	}
}

func TestWriteThenReadPIDFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yolorouter.db.pid")
	if err := writePIDFile(path); err != nil {
		t.Fatalf("writePIDFile: %v", err)
	}
	pid, err := readPIDFile(path)
	if err != nil {
		t.Fatalf("readPIDFile: %v", err)
	}
	if pid != os.Getpid() {
		t.Fatalf("pid = %d, want %d", pid, os.Getpid())
	}
}

func TestReadPIDFileMissing(t *testing.T) {
	_, err := readPIDFile(filepath.Join(t.TempDir(), "absent.pid"))
	if err == nil {
		t.Fatal("expected error for missing pid file")
	}
}

func TestReadPIDFileInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.pid")
	if err := os.WriteFile(path, []byte("not-a-number\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPIDFile(path); err == nil {
		t.Fatal("expected error for non-numeric pid")
	}
	// zero / negative pids are invalid too
	if err := os.WriteFile(path, []byte(strconv.Itoa(0)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPIDFile(path); err == nil {
		t.Fatal("expected error for zero pid")
	}
}
