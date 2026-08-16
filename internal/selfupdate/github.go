// Release metadata and asset handling: fetching the latest GitHub release,
// downloading assets (optionally through a mirror), matching the platform
// archive, checksum verification, and extracting the binary out of the
// tar.gz.
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yolorouter/yolorouter/internal/version"
)

// Asset mirrors the relevant fields of GitHub's release-asset JSON.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Release mirrors the relevant fields of GitHub's release JSON. The updater
// needs assets (to find the per-platform archive + checksums.txt), unlike
// the version-check service which only reads tag_name/html_url.
type Release struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

// FetchLatestRelease looks up the repository's latest release. An explicit
// proxy routes the request through that mirror alone; with no proxy the
// lookup tries GitHub directly and falls back to the built-in mirror when
// direct fails (see routeSet).
func FetchLatestRelease(ctx context.Context, client *http.Client, repo, proxy string) (*Release, error) {
	return newRouteSet(proxy).fetchLatest(ctx, client, repo)
}

// fetchLatestReleaseVia is one route's lookup attempt.
func fetchLatestReleaseVia(ctx context.Context, client *http.Client, repo, proxy string) (*Release, error) {
	url := version.ProxyURL(proxy, fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "yolorouter")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github returned %d for %s", resp.StatusCode, redactURL(url))
	}
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release JSON: %w", err)
	}
	return &rel, nil
}

// Stall-detection tuning. Package variables (not constants) so tests can
// shrink the windows; production never changes them.
var (
	// stallSampleInterval is how often the watchdog re-reads the byte count.
	stallSampleInterval = time.Second
	// stallNoProgressLimit aborts a download that received NOTHING for this
	// long — a dead connection that TCP keeps nominally open.
	stallNoProgressLimit = 30 * time.Second
	// stallProjectionAfter is the warm-up before the watchdog starts judging
	// the transfer rate: enough time for TLS + slow-start to settle so a
	// healthy download is never condemned on its first seconds.
	stallProjectionAfter = 15 * time.Second
)

// errDownloadStalled marks a download the watchdog aborted for SILENCE — no
// response headers, or no body bytes, for stallNoProgressLimit. Silence
// means the connection is dead; the route is not worth retrying.
var errDownloadStalled = errors.New("transfer stalled")

// errTransferTooSlow marks a download the watchdog aborted on the rate
// projection: bytes were arriving, but too slowly to finish the announced
// size within the budget. Distinct from errDownloadStalled because it is a
// heuristic, not proof — the route walk uses this distinction to give such a
// route one projection-free second chance after every other route fails.
var errTransferTooSlow = errors.New("transfer too slow to finish in time")

// transferDoomed reports whether a transfer that moved n of total body bytes
// in bodyElapsed can no longer finish before the caller's budget kills it.
// headerDelay is what connection setup and headers already spent of that
// budget — the client's timeout clock started before the first body byte, so
// the body only has the remainder. Unknown totals (<= 0) and clock-less
// clients (budget <= 0) are never doomed: with no total or no deadline there
// is no proof of failure.
func transferDoomed(bodyElapsed, headerDelay, budget time.Duration, n, total int64) bool {
	if total <= 0 || n <= 0 || budget <= 0 {
		return false
	}
	remaining := budget - headerDelay
	if remaining <= 0 {
		// The pre-body phase alone consumed the budget; the client's own
		// timeout is already due.
		return true
	}
	projected := time.Duration(float64(bodyElapsed) * float64(total) / float64(n))
	return projected > remaining
}

// countingReader lets the watchdog goroutine observe download progress.
type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// downloadVia is one route's download attempt, guarded by a stall watchdog.
//
// The client's own timeout only bounds the TOTAL duration — a trickling CDN
// (bytes still arriving, far too slowly to ever finish) would burn the whole
// budget before failing, and only then would the caller try the next route.
// The watchdog instead cancels as soon as the transfer is provably doomed:
// response headers that never arrive, no body bytes at all for
// stallNoProgressLimit, or a measured rate that cannot deliver the announced
// Content-Length within the client's timeout. It only condemns what could
// never have succeeded anyway, so slow-but-sufficient links are never cut
// off. Two cases deliberately ride out the client timeout instead: a
// response WITHOUT a Content-Length (no total, no proof of doom — a guessed
// rate threshold could cut a healthy slow link), and any attempt made with
// noProjection: the projection's only purpose is to reach another route
// sooner, so the walk turns it off when there is nothing left to switch to —
// the last route of a pass, and every projection-free retry — because a kill
// there would fail an update that a bursty-but-viable transfer (slow
// warm-up, then full speed) was still going to finish. The certain-death
// guards (no headers, no bytes) apply on every attempt.
func downloadVia(ctx context.Context, client *http.Client, proxy, rawURL string, noProjection bool) ([]byte, error) {
	// The projection budget is whatever clock kills this attempt first: the
	// client's own timeout (restarted per request) or the route walk's
	// remaining context deadline — a later route in the same walk only has
	// what the earlier ones left. No clock at all means no projection; only
	// the no-progress guards apply. The clock starts where the client's does
	// — before the request — so time spent on connection setup and headers
	// counts against what remains for the body.
	budget := client.Timeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); budget <= 0 || remaining < budget {
			budget = remaining
		}
	}
	attemptStart := time.Now()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	url := version.ProxyURL(proxy, rawURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// Why a watchdog cancelled, if one did. The two reasons carry different
	// meanings for the route walk — silence is a dead route, a failed
	// projection is a heuristic worth a second chance — so they surface as
	// different sentinels. ONE flag serves both the header phase and the
	// body phase: a timer that fires just as the headers squeak in would
	// otherwise cancel the body read with no phase's flag set, surfacing a
	// bare context error instead of the stall it was.
	const (
		reasonSilence = 1
		reasonTooSlow = 2
	)
	var received atomic.Int64
	var stallReason atomic.Int32

	// The body watchdog below can only start once headers have arrived —
	// client.Do blocks until then. A route that accepts the connection and
	// then never sends a response line is neither a connect failure nor a
	// body stall, so without this it would sit out the client's whole
	// timeout before the next route got a chance. Same silence, same limit:
	// no headers for stallNoProgressLimit is a stall.
	headerWatch := time.AfterFunc(stallNoProgressLimit, func() {
		stallReason.Store(reasonSilence)
		cancel()
	})
	resp, err := client.Do(req)
	headerWatch.Stop()
	if err != nil {
		if stallReason.Load() == reasonSilence {
			return nil, fmt.Errorf("%w: no response headers within %v", errDownloadStalled, stallNoProgressLimit)
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d for %s", resp.StatusCode, redactURL(url))
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(stallSampleInterval)
		defer ticker.Stop()
		bodyStart := time.Now()
		headerDelay := bodyStart.Sub(attemptStart)
		var lastBytes int64
		lastProgress := bodyStart
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}
			n := received.Load()
			now := time.Now()
			if n > lastBytes {
				lastBytes = n
				lastProgress = now
			} else if now.Sub(lastProgress) >= stallNoProgressLimit {
				stallReason.Store(reasonSilence)
				cancel()
				return
			}
			if !noProjection && now.Sub(bodyStart) >= stallProjectionAfter &&
				transferDoomed(now.Sub(bodyStart), headerDelay, budget, n, resp.ContentLength) {
				stallReason.Store(reasonTooSlow)
				cancel()
				return
			}
		}
	}()

	data, err := io.ReadAll(countingReader{r: resp.Body, n: &received})
	if err != nil {
		if reason := stallReason.Load(); reason != 0 {
			sentinel := errDownloadStalled
			if reason == reasonTooSlow {
				sentinel = errTransferTooSlow
			}
			if total := resp.ContentLength; total > 0 {
				return nil, fmt.Errorf("%w after %d of %d bytes", sentinel, received.Load(), total)
			}
			return nil, fmt.Errorf("%w after %d bytes (total size unknown)", sentinel, received.Load())
		}
		return nil, err
	}
	return data, nil
}

// matchAsset finds the archive for the current platform in the release's
// assets, named per .goreleaser.yaml's archive name_template:
// yolorouter_v{ver}_{goos}_{goarch}.tar.gz (the leading v matches the
// GitHub tag_name, since the updater resolves ver from tag_name).
func matchAsset(assets []Asset, goos, goarch, ver string) (Asset, error) {
	want := fmt.Sprintf("yolorouter_%s_%s_%s.tar.gz", ver, goos, goarch)
	return findAsset(assets, want)
}

func findAsset(assets []Asset, name string) (Asset, error) {
	for _, a := range assets {
		if a.Name == name {
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("no asset named %q in release; available: %s", name, assetNames(assets))
}

func assetNames(assets []Asset) string {
	names := make([]string, len(assets))
	for i, a := range assets {
		names[i] = a.Name
	}
	return "[" + strings.Join(names, ", ") + "]"
}

// verifyChecksum recomputes the SHA256 of the downloaded archive and compares
// it against the entry goreleaser's checksums.txt carries for that asset. The
// checksums.txt format is one "<sha256>  <name>" line per asset.
func verifyChecksum(assetBytes []byte, assetName string, checksumsTxt []byte) error {
	sum := sha256.Sum256(assetBytes)
	got := hex.EncodeToString(sum[:])
	want, ok := parseChecksums(checksumsTxt)[assetName]
	if !ok {
		return fmt.Errorf("checksums.txt has no entry for %q", assetName)
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("SHA256 mismatch for %q: downloaded %s, expected %s", assetName, got, want)
	}
	return nil
}

func parseChecksums(txt []byte) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(string(txt), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			m[fields[1]] = fields[0]
		}
	}
	return m
}

// extractBinary unpacks the tar.gz archive and returns the bytes of the
// executable (basename binaryName), wherever the archive placed it.
func extractBinary(assetBytes []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(assetBytes))
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar entry: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == binaryName {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read %s from tar: %w", hdr.Name, err)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("binary %q not found in archive", binaryName)
}

// isExecutable sniffs the leading magic bytes for the executable formats
// release builds ship (ELF for linux, Mach-O for darwin). A plain non-zero
// check isn't enough: a corrupt extraction that produced garbage bytes would
// otherwise be chmod'd executable and silently installed.
func isExecutable(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	// ELF: 0x7f 'E' 'L' 'F'
	if data[0] == 0x7f && data[1] == 'E' && data[2] == 'L' && data[3] == 'F' {
		return true
	}
	// Mach-O magic numbers (32/64-bit, little/big-endian variants).
	magic := [4]byte{data[0], data[1], data[2], data[3]}
	for _, m := range [][4]byte{
		{0xfe, 0xed, 0xfa, 0xce}, // MH_MAGIC (BE, 32-bit)
		{0xfe, 0xed, 0xfa, 0xcf}, // MH_MAGIC_64 (BE, 64-bit)
		{0xce, 0xfa, 0xed, 0xfe}, // MH_CIGAM (LE, 32-bit)
		{0xcf, 0xfa, 0xed, 0xfe}, // MH_CIGAM_64 (LE, 64-bit)
	} {
		if magic == m {
			return true
		}
	}
	return false
}
