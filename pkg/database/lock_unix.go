//go:build !windows

package database

import (
	"os"
	"syscall"
)

// lockFileExclusiveNonBlocking takes an exclusive advisory lock on f without
// blocking (LOCK_NB), failing immediately if another process holds it.
func lockFileExclusiveNonBlocking(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
