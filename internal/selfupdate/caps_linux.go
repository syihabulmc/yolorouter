//go:build linux

package selfupdate

import "golang.org/x/sys/unix"

// executableHasCapabilities reports whether the file carries Linux file
// capabilities (the security.capability xattr, e.g. cap_net_bind_service set
// via setcap). The atomic-rename replacement installs a fresh inode and
// xattrs do not carry over, so a capability-bearing binary must not be
// auto-restarted onto its replacement — the new process would silently lose
// the capability (typically failing to bind its privileged port).
//
// A probe error reads as "no capabilities": filesystems without xattr
// support would otherwise block every update on a feature they cannot even
// express.
func executableHasCapabilities(path string) bool {
	size, err := unix.Getxattr(path, "security.capability", nil)
	return err == nil && size > 0
}
