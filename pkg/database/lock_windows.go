//go:build windows

package database

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFileExclusiveNonBlocking takes an exclusive lock on the first byte of f
// without blocking. LOCKFILE_FAIL_IMMEDIATELY mirrors flock's LOCK_NB so a
// second instance fails fast instead of waiting.
func lockFileExclusiveNonBlocking(f *os.File) error {
	h := windows.Handle(f.Fd())
	return windows.LockFileEx(
		h,
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0,
		new(windows.Overlapped),
	)
}

func unlockFile(f *os.File) error {
	h := windows.Handle(f.Fd())
	return windows.UnlockFileEx(h, 0, 1, 0, new(windows.Overlapped))
}
