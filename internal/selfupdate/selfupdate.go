// Package selfupdate implements the in-place binary upgrade: resolve the
// latest GitHub release, verify its checksum, and atomically replace the
// running executable. The mechanics live here — outside cmd — because two
// callers share them: the `yolorouter update` CLI command (interactive, with
// a confirmation prompt) and the admin console's update endpoint
// (non-interactive, followed by a scheduled process restart). The
// replacement sequence is security-sensitive and must exist exactly once.
package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/mod/semver"

	"github.com/yolorouter/yolorouter/internal/version"
)

// githubAPITimeout bounds the release-metadata lookup. Update runs are
// user-initiated, so a stalled GitHub must fail within a bounded wait rather
// than hang.
const githubAPITimeout = 30 * time.Second

// downloadTimeout bounds each asset download. Asset downloads (binary
// archive + checksums.txt) can be tens of MB; the 30s metadata timeout would
// fail them on slower links, so downloads get their own longer budget.
const downloadTimeout = 5 * time.Minute

// binaryName is the executable filename inside a goreleaser-produced
// archive (set by .goreleaser.yaml's builds.binary). extractBinary looks for
// this basename regardless of any parent directory the archive wraps it in.
const binaryName = "yolorouter"

// EnvContainer is the environment variable a container image sets to mark
// this process as running inside a container. In-place binary replacement is
// wrong there — the image is immutable and a replaced binary vanishes on the
// next container recreate — so both update entry points refuse to apply and
// point the operator at pulling a newer image instead. Version *checking* is
// unaffected: knowing an update exists is just as useful in a container.
const EnvContainer = "YOLOROUTER_IN_DOCKER"

// InContainer reports whether the process was marked as running inside a
// container (see EnvContainer).
func InContainer() bool {
	return os.Getenv(EnvContainer) != ""
}

// Update-mode values reported by the system-version endpoint and used to gate
// the apply endpoint. Exactly one applies to a running process; only
// ModeInPlace permits applying an update through this package.
const (
	// ModeInPlace: a release build on a unix host with updates enabled —
	// Apply is available.
	ModeInPlace = "in_place"
	// ModeContainer: running inside a container image; upgrade by pulling a
	// newer image.
	ModeContainer = "container"
	// ModeWindows: windows cannot atomically replace a running .exe;
	// upgrade by downloading the release manually.
	ModeWindows = "windows"
	// ModeDisabled: updates are disabled by configuration (no resolvable
	// GitHub repo).
	ModeDisabled = "disabled"
	// ModeDevBuild: the running binary is not an exact-tag release build,
	// so there is no safe version comparison to update against.
	ModeDevBuild = "dev"
	// ModeCapabilities: the binary carries Linux file capabilities (setcap),
	// which the atomic replacement cannot preserve — an automatic restart
	// would bring up a binary that lost them (e.g. can no longer bind its
	// privileged port). Upgrade via the CLI, which leaves the restart to the
	// operator and prints the reapply guidance.
	ModeCapabilities = "capabilities"
)

// capsProbe is the capability check used by Mode and Apply; a package-level
// hook so tests can simulate a capability-bearing binary on platforms where
// the xattr cannot be set.
var capsProbe = executableHasCapabilities

// resolveExecutable returns the real path of the running binary.
// os.Executable may return a symlink path on macOS (its contract permits
// it); operating on the link would replace the symlink, not the real target
// a service invokes, so the service would stay on the old binary despite
// reported success.
func resolveExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, evalErr := filepath.EvalSymlinks(exe); evalErr == nil && real != "" {
		exe = real
	}
	return exe, nil
}

// Mode classifies how this process can be upgraded. repo is the resolved
// update repository ("" = updates disabled); current is the running
// version string.
func Mode(repo, current string) string {
	switch {
	case InContainer():
		return ModeContainer
	case runtime.GOOS == "windows":
		return ModeWindows
	case repo == "":
		return ModeDisabled
	case currentUpdatable(current) != nil:
		return ModeDevBuild
	default:
		// A probe failure (unresolvable executable) reads as "no
		// capabilities": the same conservative-permissive stance as the
		// probe itself, and Apply re-checks before touching anything.
		if exe, err := resolveExecutable(); err == nil && capsProbe(exe) {
			return ModeCapabilities
		}
		return ModeInPlace
	}
}

// ErrCancelled is returned by Apply when the Confirm callback declined the
// upgrade. It is a clean abort, not a failure — nothing was modified.
var ErrCancelled = errors.New("update cancelled")

// Options configures one Apply run.
type Options struct {
	// Repo is the "owner/name" GitHub repository releases are fetched from.
	Repo string
	// Proxy optionally routes the release lookup and asset downloads
	// through a mirror (deployments where GitHub is slow or blocked).
	Proxy string
	// Current is the running version (an exact-tag semver like "v1.2.3");
	// Apply refuses anything else, see currentUpdatable.
	Current string
	// Confirm, when non-nil, is called after the new binary has been
	// downloaded and verified but before anything on disk is touched.
	// Returning false aborts with ErrCancelled. A nil Confirm applies
	// without asking.
	Confirm func(current, target string) bool
	// AllowCapabilityLoss permits replacing a binary that carries Linux
	// file capabilities (which the rename cannot preserve). Only a caller
	// that leaves the restart to the operator — the CLI, which prints the
	// reapply guidance — should set this; the auto-restarting update
	// endpoint must not, or the restarted binary silently loses the
	// capability.
	AllowCapabilityLoss bool

	// exePath overrides executable resolution in tests, where replacing the
	// running test binary is not an option.
	exePath string
}

// Result reports what Apply did.
type Result struct {
	// Target is the release tag that was installed (or already running when
	// UpToDate is true).
	Target string
	// UpToDate is true when the latest release is not newer than Current;
	// nothing was downloaded or modified.
	UpToDate bool
	// BackupPath is the pre-upgrade binary copy ("<exe>.bak"), valid for a
	// rollback only until the process restarts on the new binary.
	BackupPath string
}

// Apply performs one full upgrade: resolve the latest release, download and
// verify the platform archive, and atomically replace the current
// executable. It deliberately needs no database connection — an upgrade only
// touches the binary; the restart afterwards runs migrations.
//
// The sequence's re-validation points are deliberate: the executable's
// identity is captured before any network I/O, re-checked after the
// confirmation gap, and re-checked again immediately before the rename,
// with an inter-process lock serializing overlapping updaters.
func Apply(ctx context.Context, opts Options) (Result, error) {
	// Windows can't atomically replace a running .exe (the file is locked),
	// so automatic update is unsupported there. Callers gate on Mode first;
	// this guard keeps the invariant even for a caller that forgot.
	if runtime.GOOS == "windows" {
		return Result{}, fmt.Errorf("in-place update is unsupported on windows; download the latest release manually")
	}
	if InContainer() {
		return Result{}, fmt.Errorf("in-place update is disabled inside a container; pull the newer image and recreate the container")
	}
	if opts.Repo == "" {
		return Result{}, fmt.Errorf("update is disabled: no update repository is configured")
	}
	if err := currentUpdatable(opts.Current); err != nil {
		return Result{}, err
	}

	// Capture the executable's identity BEFORE any network lookup or
	// download: a concurrent updater or package manager that installs a
	// newer binary during the release lookup or asset download would
	// otherwise be missed by a post-download stat (it'd record the newer
	// binary as the baseline, the later re-stat would pass, and our stale
	// downloaded bytes would overwrite the newer install).
	exe := opts.exePath
	if exe == "" {
		resolved, err := resolveExecutable()
		if err != nil {
			return Result{}, fmt.Errorf("resolve current executable: %w", err)
		}
		exe = resolved
	}
	initialInfo, err := os.Stat(exe)
	if err != nil {
		return Result{}, fmt.Errorf("stat current executable: %w", err)
	}

	// The atomic rename installs a fresh inode and drops any Linux file
	// capabilities (security.capability xattr). A caller that will restart
	// automatically must refuse here — the restarted binary would silently
	// lose the capability (e.g. no longer bind its privileged port).
	if !opts.AllowCapabilityLoss && capsProbe(exe) {
		return Result{}, fmt.Errorf("the current binary carries Linux file capabilities, which the replacement cannot preserve; update via `yolorouter update` and reapply them before restarting")
	}

	client := &http.Client{Timeout: githubAPITimeout}
	rel, err := FetchLatestRelease(ctx, client, opts.Repo, opts.Proxy)
	if err != nil {
		return Result{}, fmt.Errorf("look up latest release: %w", err)
	}

	// A non-semver latest tag (empty tag_name, or a release whose tag isn't
	// semver) makes Compare treat it as older than current, reporting a
	// misleading "already up to date". Surface a config/release error
	// instead.
	if !semver.IsValid(rel.TagName) {
		return Result{}, fmt.Errorf("latest release tag %q is not a valid semver; check update.github_repo / release source configuration", rel.TagName)
	}
	// Reject a prerelease latest (e.g. v1.3.0-rc1 published as
	// /releases/latest): installing it would make current a prerelease,
	// which currentUpdatable then refuses to advance from — the user gets
	// stuck on the RC. The updater only installs exact-tag stable releases.
	if semver.Prerelease(rel.TagName) != "" {
		return Result{}, fmt.Errorf("latest release %q is a prerelease; the updater only installs stable tags. Pin to a stable release or wait for it", rel.TagName)
	}
	if semver.Compare(rel.TagName, opts.Current) <= 0 {
		return Result{Target: opts.Current, UpToDate: true}, nil
	}

	asset, err := matchAsset(rel.Assets, runtime.GOOS, runtime.GOARCH, rel.TagName)
	if err != nil {
		return Result{}, err
	}
	checksumsAsset, err := findAsset(rel.Assets, "checksums.txt")
	if err != nil {
		return Result{}, err
	}

	// The archive and checksums.txt are held in memory (download returns
	// []byte) and the staged binary is written beside the executable, so the
	// updater does not need a writable temp directory — allocating one would
	// make updates fail on systems where TMPDIR is read-only or absent even
	// though the actual writes never touch it.
	// GitHub returns absolute github.com asset URLs; route them through the
	// mirror too so a blocked-GitHub deployment can actually fetch the bytes.
	downloadClient := &http.Client{Timeout: downloadTimeout}
	assetBytes, err := download(ctx, downloadClient, version.ProxyURL(opts.Proxy, asset.BrowserDownloadURL))
	if err != nil {
		return Result{}, fmt.Errorf("download %s: %w", asset.Name, err)
	}
	checksumsBytes, err := download(ctx, downloadClient, version.ProxyURL(opts.Proxy, checksumsAsset.BrowserDownloadURL))
	if err != nil {
		return Result{}, fmt.Errorf("download checksums.txt: %w", err)
	}

	// SHA256 verify BEFORE extracting or replacing: a tampered or truncated
	// download must never reach the binary path. A failed check is a hard
	// stop, never "replace anyway".
	if err := verifyChecksum(assetBytes, asset.Name, checksumsBytes); err != nil {
		return Result{}, fmt.Errorf("checksum verification failed, not replacing: %w", err)
	}

	binaryBytes, err := extractBinary(assetBytes)
	if err != nil {
		return Result{}, fmt.Errorf("extract binary from %s: %w", asset.Name, err)
	}
	// The archive's checksum is over the tar.gz, not the inner binary — an
	// independent magic-number check guards against a corrupt archive that
	// unpacked to something other than the expected executable.
	if !isExecutable(binaryBytes) {
		return Result{}, fmt.Errorf("extracted binary from %s is not a recognized executable format", asset.Name)
	}

	if opts.Confirm != nil && !opts.Confirm(opts.Current, rel.TagName) {
		return Result{}, ErrCancelled
	}

	// Acquire an inter-process lock before revalidation + replacement: two
	// overlapping updaters both pass a single re-stat against the original
	// binary, then race to replaceBinary — the loser would rename its stale
	// downloaded bytes over the newer binary the winner installed, silently
	// downgrading. The lock serializes revalidation+replace so the second
	// acquirer aborts.
	lockPath, lock, err := acquireUpdateLock(filepath.Dir(exe))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = lock.Close(); _ = os.Remove(lockPath) }()

	// Re-stat before replacing: if the executable changed during the
	// confirmation gap (another update installed a newer binary), our
	// downloaded bytes are now stale — abort rather than downgrade.
	info, err := os.Stat(exe)
	if err != nil {
		return Result{}, fmt.Errorf("re-stat current executable: %w", err)
	}
	if !sameExeIdentity(initialInfo, info) {
		return Result{}, fmt.Errorf("executable changed during confirmation (another update may have run); aborting to avoid downgrade. Retry the update")
	}

	staging, err := writeStagedBinary(filepath.Dir(exe), binaryBytes, info.Mode())
	if err != nil {
		return Result{}, fmt.Errorf("write new binary: %w", err)
	}

	// This stat only captures the live binary's ownership/mode for the
	// chown below — the identity re-validation against initialInfo (did an
	// external tool replace the binary during staging?) happens inside
	// replaceBinary immediately before the rename, so it is not repeated
	// here.
	preRename, err := os.Stat(exe)
	if err != nil {
		_ = os.Remove(staging)
		return Result{}, fmt.Errorf("re-stat current executable before replace: %w", err)
	}
	// Preserve the original uid/gid
	// (e.g. root:yolorouter) — writeStagedBinary created the staging as
	// root:root under sudo, and a service account in the original group
	// couldn't execute the replaced binary. chown clears setuid/setgid on
	// Unix, so reapply the mode afterward.
	if uid, gid := ownershipOf(preRename); uid >= 0 || gid >= 0 {
		if err := os.Chown(staging, uid, gid); err != nil {
			_ = os.Remove(staging)
			return Result{}, fmt.Errorf("chown staging to match executable: %w", err)
		}
		if err := os.Chmod(staging, preRename.Mode()); err != nil {
			_ = os.Remove(staging)
			return Result{}, fmt.Errorf("reapply staging mode after chown: %w", err)
		}
		// chown/chmod dirtied inode metadata AFTER writeStagedBinary's
		// Sync+Close, so fsync the staging file again — otherwise a crash
		// before the rename could lose the ownership/mode change.
		if f, syncErr := os.Open(staging); syncErr == nil {
			_ = f.Sync()
			_ = f.Close()
		}
	}

	backupPath, err := replaceBinary(exe, staging, initialInfo)
	if err != nil {
		_ = os.Remove(staging)
		return Result{}, err
	}

	return Result{Target: rel.TagName, BackupPath: backupPath}, nil
}
