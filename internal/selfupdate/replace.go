// On-disk replacement mechanics: version eligibility, executable identity
// checks, the inter-process update lock, staged writes, and the atomic
// backup+rename. The ordering of these steps carries security guarantees —
// read the function comments before changing it.
package selfupdate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"golang.org/x/mod/semver"
)

// currentUpdatable rejects a current version the updater cannot safely
// compare against the latest tag: a non-semver "dev" build, and — the case
// that bit git-describe release builds — a prerelease. git-describe strings
// ("v1.2.3-dirty", "v1.2.3-4-gabc") and release candidates ("v1.2.3-rc1")
// are semver prereleases ranked BELOW their release, so an unguarded
// Compare(latest, current) would report the tag as newer and let the updater
// replace a newer dirty build with the older tag. Only exact-tag release
// binaries ship, so only an exact-tag current is eligible to update.
func currentUpdatable(current string) error {
	if !semver.IsValid(current) {
		return fmt.Errorf("cannot update a non-release build (current version %q); install a release build first", current)
	}
	if semver.Prerelease(current) != "" {
		return fmt.Errorf("cannot update a pre-release or git-describe build (current version %q); checkout the exact release tag and rebuild", current)
	}
	return nil
}

// sameExeIdentity reports whether two FileInfo describe the same on-disk
// executable state. mtime + size is a pragmatic proxy for "did another
// updater replace this binary": a rename installs a fresh inode whose mtime
// differs from the one recorded at release-selection time. It isn't a perfect
// identity (same-second + same-size collisions), but it's enough to catch the
// common overlap without a cross-platform file lock.
func sameExeIdentity(a, b os.FileInfo) bool {
	return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// ownershipOf extracts the uid/gid of the file described by info, or returns
// (-1, -1) when not available (non-unix, or Sys() doesn't expose them). -1
// means "don't change" to os.Chown. Reflection avoids a platform-specific
// syscall.Stat_t type assertion that wouldn't compile on windows (where
// update is a no-op anyway).
func ownershipOf(info os.FileInfo) (uid, gid int) {
	if info == nil {
		return -1, -1
	}
	sys := info.Sys()
	if sys == nil {
		return -1, -1
	}
	v := reflect.ValueOf(sys)
	// info.Sys() returns a *syscall.Stat_t (a pointer); FieldByName needs the
	// struct value, so deref first. Bail to "don't change" for anything that
	// isn't a struct (non-unix, or unexpected underlying type).
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return -1, -1
	}
	uidV := v.FieldByName("Uid")
	gidV := v.FieldByName("Gid")
	if !uidV.IsValid() || !gidV.IsValid() {
		return -1, -1
	}
	// Uid/Gid are uint32 on linux/darwin, but other platforms may use int —
	// pick the right accessor by Kind rather than assuming Int()/Uint().
	toInt := func(field reflect.Value) int {
		switch field.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return int(field.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return int(field.Uint())
		default:
			return -1
		}
	}
	uid, gid = toInt(uidV), toInt(gidV)
	if uid < 0 || gid < 0 {
		return -1, -1
	}
	return uid, gid
}

// acquireUpdateLock takes an inter-process exclusive lock for the update
// operation, so two overlapping updaters can't both pass revalidation and
// then race to replaceBinary (the loser would rename its stale downloaded
// bytes over the newer binary the winner just installed, silently
// downgrading). The lock is a same-directory file created with
// O_CREATE|O_EXCL (atomic on POSIX); held through backup+rename, released on
// return. A crash leaves the lock file behind — the error message tells the
// operator to remove it.
func acquireUpdateLock(dir string) (string, *os.File, error) {
	lockPath := filepath.Join(dir, ".yolorouter-update.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return lockPath, nil, fmt.Errorf("another update appears to be in progress (lock %s); aborting. If no update is running, remove that file and retry", lockPath)
		}
		// EACCES (dir not writable), EROFS (read-only fs), etc. — NOT a lock
		// collision. Wrap distinctly so the operator fixes the real cause
		// rather than being told to remove a lock that was never created.
		return lockPath, nil, fmt.Errorf("create update lock %s: %w (check directory write permissions / filesystem)", lockPath, err)
	}
	_, _ = fmt.Fprintf(f, "pid=%d\n", os.Getpid())
	return lockPath, f, nil
}

// writeStagedBinary writes data to a uniquely-named, exclusively-created
// temporary file in dir, fsyncs, and closes — so the bytes are durable on
// disk before the atomic rename. It returns the temp path for the caller to
// rename over the live binary.
//
// os.CreateTemp (O_CREATE|O_EXCL, randomized name) is used rather than a
// fixed "<exe>.new" path: two overlapping updaters would otherwise truncate
// the same inode, and a predictable path in a shared/sticky directory can be
// symlink-clobbered to overwrite an arbitrary privileged file. The file is
// chmod'd to mode AFTER creation — CreateTemp creates with 0600 and a
// restrictive umask (e.g. 077) would otherwise strip the executable bit from
// an os.OpenFile mode arg, leaving the replaced binary unrunnable under a
// service account. On any error the file is removed so a
// half-written staging file can never be mistaken for a valid upgrade.
func writeStagedBinary(dir string, data []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(dir, "yolorouter.new.*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	cleanup := func() { _ = f.Close(); _ = os.Remove(path) }
	if _, err := f.Write(data); err != nil {
		cleanup()
		return "", err
	}
	// Apply the executable mode BEFORE the final Sync so the persisted file
	// already carries it — CreateTemp creates with 0600, and a chmod after
	// Sync dirties inode metadata without a follow-up fsync, so a crash could
	// leave the installed binary non-executable. f.Chmod on the open handle
	// also avoids umask masking an os.OpenFile mode arg.
	if err := f.Chmod(mode); err != nil {
		cleanup()
		return "", err
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// replaceBinary backs up the current binary to currentPath+".bak" and then
// atomically renames the staging path over it. On linux/mac the rename
// overwrites the running binary's path while the running process keeps the
// old inode until restart. The parent directory is fsync'd
// so the rename itself survives a crash.
func replaceBinary(currentPath, stagingPath string, initialInfo os.FileInfo) (string, error) {
	backupPath := currentPath + ".bak"
	if err := copyFile(currentPath, backupPath); err != nil {
		return "", fmt.Errorf("backup current binary: %w", err)
	}
	// Re-validate immediately before the rename: an external installer could
	// have replaced the binary during the backup copy (the update lock only
	// serializes update-vs-update). If it changed since initialInfo, abort
	// rather than overwrite the newer installation.
	preRename, err := os.Stat(currentPath)
	if err != nil {
		return "", fmt.Errorf("re-stat current executable before rename: %w", err)
	}
	if !sameExeIdentity(initialInfo, preRename) {
		return "", fmt.Errorf("executable changed during backup (external tool may have updated it); aborting to avoid downgrade. Retry the update")
	}
	if err := os.Rename(stagingPath, currentPath); err != nil {
		return "", fmt.Errorf("replace binary: %w", err)
	}
	// Parent-dir fsync is best-effort: the rename is already committed, so a
	// failure only weakens crash-durability, not the upgrade itself. Don't
	// return an error — that would fail the update after an irreversible
	// mutation, leaving the operator with neither success nor rollback
	// guidance.
	_ = fsyncDir(filepath.Dir(currentPath))
	return backupPath, nil
}

// copyFile copies src to dst preserving src's mode, via a uniquely-named,
// exclusively-created temp file in dst's directory + rename. The temp is
// chmod'd to src's mode after creation (umask-safe). This mirrors
// writeStagedBinary's hardening: an interrupted copy can't leave a truncated
// .bak a user might mistake for a rollback, two overlapping copies can't
// truncate each other, and a predictable .bak.tmp can't be symlink-clobbered
// in a sticky dir.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp.*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	cleanup := func() { _ = out.Close(); _ = os.Remove(tmp) }
	if _, err := io.Copy(out, in); err != nil {
		cleanup()
		return err
	}
	// Preserve src's uid/gid (e.g. root:yolorouter) — CreateTemp created the
	// .bak as root:root under sudo, and an advertised rollback would be
	// unrunnable under the original service account. chown BEFORE chmod:
	// chown clears setuid/setgid on Unix, so apply ownership first then let
	// the chmod re-establish any special bits.
	if uid, gid := ownershipOf(info); uid >= 0 || gid >= 0 {
		if err := os.Chown(tmp, uid, gid); err != nil {
			cleanup()
			return err
		}
	}
	// chmod BEFORE Sync so the persisted temp already carries src's mode — a
	// chmod after Sync dirties metadata without a follow-up fsync.
	if err := out.Chmod(info.Mode()); err != nil {
		cleanup()
		return err
	}
	if err := out.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		// Remove the fully-written temp so repeated update attempts don't
		// leak another binary-sized file beside the executable.
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
