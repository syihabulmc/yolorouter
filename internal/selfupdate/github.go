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
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

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

// FetchLatestRelease looks up the repository's latest release, routing the
// request through proxy when set.
func FetchLatestRelease(ctx context.Context, client *http.Client, repo, proxy string) (*Release, error) {
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
		return nil, fmt.Errorf("github returned %d for %s", resp.StatusCode, url)
	}
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release JSON: %w", err)
	}
	return &rel, nil
}

func download(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d for %s", resp.StatusCode, url)
	}
	return io.ReadAll(resp.Body)
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
