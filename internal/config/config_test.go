package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestLoadGeneratesDefaultConfigWhenMissing(t *testing.T) {
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
		t.Fatalf("Load with missing default config should succeed: %v", err)
	}
	if cfg.Database.Driver != "sqlite" {
		t.Fatalf("expected default driver sqlite, got %s", cfg.Database.Driver)
	}
	if cfg.Security.ProviderMasterKey == "" {
		t.Fatalf("expected generated provider_master_key, got empty string")
	}

	generatedPath := filepath.Join(dir, "configs", "config.yaml")
	if _, err := os.Stat(generatedPath); err != nil {
		t.Fatalf("expected configs/config.yaml to be written: %v", err)
	}

	// The second load must reuse the same key
	cfg2, err := Load("")
	if err != nil {
		t.Fatalf("second Load failed: %v", err)
	}
	if cfg2.Security.ProviderMasterKey != cfg.Security.ProviderMasterKey {
		t.Fatalf("provider_master_key changed between loads: %q vs %q", cfg.Security.ProviderMasterKey, cfg2.Security.ProviderMasterKey)
	}
}

// TestLoadSeedsGitHubProxyFromEnv covers a mirror install: install.sh exports
// YOLO_UPDATE_GITHUB_PROXY, and the first generated config must record it under
// update.github_proxy so self-update uses the mirror without any manual edit.
// The strict re-parse of the written file also proves github_proxy is a known
// field, so a documented manual edit does not trip KnownFields(true).
func TestLoadSeedsGitHubProxyFromEnv(t *testing.T) {
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
	t.Setenv("YOLO_UPDATE_GITHUB_PROXY", "https://gh.example.com/")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load should generate config: %v", err)
	}
	if cfg.Update.GitHubProxy != "https://gh.example.com/" {
		t.Fatalf("github_proxy = %q, want it seeded from the env var", cfg.Update.GitHubProxy)
	}

	// A strict re-parse of the just-written file must accept github_proxy.
	cfg2, err := Load("")
	if err != nil {
		t.Fatalf("strict reload of generated config failed: %v", err)
	}
	if cfg2.Update.GitHubProxy != "https://gh.example.com/" {
		t.Fatalf("github_proxy not persisted across reload: %q", cfg2.Update.GitHubProxy)
	}
}

// TestLoadRejectsMultiDocumentYAML guards against yaml.Decoder.Decode's
// single-call behavior: it only consumes the first "---"-delimited
// document in a stream, so a config.yaml with two documents would have its
// second document silently ignored — potentially hiding a value the
// file's author expected to take effect — unless loadStrict explicitly
// decodes again and requires io.EOF.
func TestLoadRejectsMultiDocumentYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "server:\n  port: 8080\n" +
		"database:\n  driver: sqlite\n  sqlite_path: ./data/x.db\n" +
		"security:\n  provider_master_key: \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"\n" +
		"---\n" +
		"server:\n  port: 9090\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error for a config file containing more than one YAML document")
	}
}

func TestLoadFailsForExplicitMissingPath(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(filepath.Join(dir, "nonexistent.yaml"))
	if err == nil {
		t.Fatalf("expected error when explicit --config path does not exist")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 8080\nnot_a_real_field: true\n"), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected strict decoding to reject unknown field")
	}
}

func TestLoadRejectsEmptyRequiredFieldInExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// provider_master_key is empty in the explicitly provided config file — must error out, not silently fill it in
	if err := os.WriteFile(path, []byte("server:\n  port: 8080\ndatabase:\n  driver: sqlite\n  sqlite_path: ./data/x.db\nsecurity:\n  provider_master_key: \"\"\n"), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error for empty provider_master_key in an explicitly provided config file")
	}
}

func TestLoadRejectsInvalidDriver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("database:\n  driver: mysql\n"), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error for unsupported driver value")
	}
}

// TestLoadRejectsInvalidLogLevel guards the log.level whitelist: pkg/logger
// silently falls back to info on an unparseable level string instead of
// erroring, so config validation is the only place a typo like "debu" gets
// caught.
func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(
		"log:\n  level: debu\n"+
			"security:\n  provider_master_key: \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"\n"), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error for unrecognized log.level value")
	}
}

// TestLoadAcceptsEveryKnownLogLevel drives all four recognized log.level
// values through validate() individually, the same way
// TestLoadAcceptsEveryKnownSSLMode does for sslmode — a single
// "one bad value is rejected" test wouldn't catch a typo in validLogLevels
// that silently rejects one of the legitimate values too.
func TestLoadAcceptsEveryKnownLogLevel(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(
				"log:\n  level: "+level+"\n"+
					"security:\n  provider_master_key: \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"\n"), 0o600); err != nil {
				t.Fatalf("write test config: %v", err)
			}

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("expected log.level %q to be accepted, got error: %v", level, err)
			}
			if cfg.Log.Level != level {
				t.Fatalf("expected log.level %q to round-trip, got %q", level, cfg.Log.Level)
			}
		})
	}
}

func TestLoadRejectsInvalidSSLModeForPostgres(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(
		"database:\n  driver: postgres\n  host: localhost\n  port: 5432\n  user: u\n  dbname: d\n  sslmode: not-a-real-mode\n"+
			"security:\n  provider_master_key: \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"\n"), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error for unrecognized database.sslmode value")
	}
}

// TestLoadAcceptsEveryKnownSSLMode drives all six libpq sslmode values
// through validate() individually — a single "one bad value is rejected"
// test wouldn't catch e.g. an off-by-one typo in validSSLModes that
// silently rejects one of the legitimate values too.
func TestLoadAcceptsEveryKnownSSLMode(t *testing.T) {
	for _, mode := range []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(
				"database:\n  driver: postgres\n  host: localhost\n  port: 5432\n  user: u\n  dbname: d\n  sslmode: "+mode+"\n"+
					"security:\n  provider_master_key: \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"\n"), 0o600); err != nil {
				t.Fatalf("write test config: %v", err)
			}

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("expected sslmode %q to be accepted, got error: %v", mode, err)
			}
			if cfg.Database.SSLMode != mode {
				t.Fatalf("expected sslmode %q to round-trip, got %q", mode, cfg.Database.SSLMode)
			}
		})
	}
}

// TestAtomicWriteConfigConcurrentRaceHasExactlyOneWinner drives many
// goroutines racing to publish distinct configs to the same path at once —
// this is the scenario a Stat-then-Rename implementation gets wrong (two
// goroutines can both observe "doesn't exist" and both proceed, with the
// last Rename silently overwriting an earlier winner's file, including its
// already-generated master key). With os.Link-based publishing, every
// non-winner must observe an "already exists" condition and defer to
// whichever goroutine's content actually landed on disk.
func TestAtomicWriteConfigConcurrentRaceHasExactlyOneWinner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	const n = 20
	keys := make([]string, n)
	errs := make([]error, n)
	done := make(chan int, n)

	for i := range n {
		key, err := randomMasterKey()
		if err != nil {
			t.Fatalf("generate test key %d: %v", i, err)
		}
		keys[i] = key
		go func() {
			cfg := defaults()
			cfg.Security.ProviderMasterKey = keys[i]
			errs[i] = atomicWriteConfig(path, cfg)
			done <- i
		}()
	}
	for range n {
		<-done
	}

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: atomicWriteConfig returned a real error (should only ever succeed or silently lose the race): %v", i, err)
		}
	}

	final, err := loadStrict(path)
	if err != nil {
		t.Fatalf("loadStrict after race: %v", err)
	}
	if !slices.Contains(keys, final.Security.ProviderMasterKey) {
		t.Fatalf("final config's key %q does not match any of the %d racing goroutines' keys — file was corrupted, not just raced", final.Security.ProviderMasterKey, n)
	}

	leftover, globErr := filepath.Glob(filepath.Join(dir, "config.yaml.*.tmp"))
	if globErr != nil {
		t.Fatalf("glob for leftover temp files: %v", globErr)
	}
	if len(leftover) != 0 {
		t.Fatalf("expected no leftover temp files, found: %v", leftover)
	}
}

// TestDefaultsSetsUpdateEnabled guards the update-feature default: defaults()
// must set Enabled=true so an auto-generated or legacy config that omits the
// whole `update` section keeps updates ON. A zero-value UpdateConfig (Enabled
// false) would silently disable the feature — exactly the regression this
// test guards against.
func TestDefaultsSetsUpdateEnabled(t *testing.T) {
	cfg := defaults()
	if !cfg.Update.Enabled {
		t.Fatalf("defaults().Update.Enabled = false, want true (omitted update section must not disable updates)")
	}
}

// TestLoadOmittedUpdateSectionDefaultsEnabled drives a config with NO
// `update:` section through Load: the strict decoder starts from defaults()
// (Enabled=true) and an absent section leaves it untouched. Without this, a
// typo in defaults() that drops the Enabled field would silently flip every
// legacy config to updates-disabled.
func TestLoadOmittedUpdateSectionDefaultsEnabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(
		"server:\n  port: 8080\ndatabase:\n  driver: sqlite\n  sqlite_path: ./data/x.db\n"+
			"security:\n  provider_master_key: \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"\n"), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("expected load to succeed: %v", err)
	}
	if !cfg.Update.Enabled {
		t.Fatalf("omitted update section must default Enabled=true, got false")
	}
}

func TestLoadAcceptsUpdateSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(
		"server:\n  port: 8080\ndatabase:\n  driver: sqlite\n  sqlite_path: ./data/x.db\n"+
			"security:\n  provider_master_key: \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"\n"+
			"update:\n  enabled: false\n  github_repo: \"fork/ce\"\n"), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("expected load to succeed: %v", err)
	}
	if cfg.Update.Enabled {
		t.Fatalf("expected Enabled=false, got true")
	}
	if cfg.Update.GitHubRepo != "fork/ce" {
		t.Fatalf("expected GitHubRepo fork/ce, got %q", cfg.Update.GitHubRepo)
	}
}

// TestLoadFillsGitHubProxyFromEnvOnExistingConfig covers a mirror installer
// upgrading a prior direct install: config.yaml already exists (so it is never
// regenerated), yet the proxy env the installer injects into the service unit
// must still take effect — but only when the file leaves github_proxy empty.
func TestLoadFillsGitHubProxyFromEnvOnExistingConfig(t *testing.T) {
	base := "server:\n  port: 8080\ndatabase:\n  driver: sqlite\n  sqlite_path: ./data/x.db\n" +
		"security:\n  provider_master_key: \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"\n"

	t.Run("empty in file is filled from env", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(path, []byte(base+"update:\n  enabled: true\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		t.Setenv("YOLO_UPDATE_GITHUB_PROXY", "https://gh.example.com/")
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.Update.GitHubProxy != "https://gh.example.com/" {
			t.Fatalf("github_proxy = %q, want filled from env", cfg.Update.GitHubProxy)
		}
	})

	t.Run("explicit value in file wins over env", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(path, []byte(base+"update:\n  enabled: true\n  github_proxy: \"https://in-file.example/\"\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		t.Setenv("YOLO_UPDATE_GITHUB_PROXY", "https://gh.example.com/")
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.Update.GitHubProxy != "https://in-file.example/" {
			t.Fatalf("github_proxy = %q, want the explicit config value to win", cfg.Update.GitHubProxy)
		}
	})
}

// TestLoadRejectsInvalidGitHubRepo drives every malformed shape through
// validate() so a typo'd owner/repo fails at config load, not as a mysterious
// GitHub 404 at runtime.
func TestLoadRejectsInvalidGitHubRepo(t *testing.T) {
	for _, repo := range []string{
		"ownerrepo",        // missing slash
		"owner/repo/extra", // too many segments
		"/repo",            // empty owner
		"owner/",           // empty repo
		"own er/repo",      // whitespace
	} {
		t.Run(repo, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(
				"server:\n  port: 8080\ndatabase:\n  driver: sqlite\n  sqlite_path: ./data/x.db\n"+
					"security:\n  provider_master_key: \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"\n"+
					"update:\n  github_repo: \""+repo+"\"\n"), 0o600); err != nil {
				t.Fatalf("write test config: %v", err)
			}
			if _, err := Load(path); err == nil {
				t.Fatalf("expected error for malformed github_repo %q", repo)
			}
		})
	}
}

// TestLoadAcceptsEmptyGitHubRepo: an empty repo is valid (it falls back to
// the compiled-in default, or disables updates if that is also empty) — only
// a non-empty malformed value is rejected.
func TestLoadAcceptsEmptyGitHubRepo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(
		"server:\n  port: 8080\ndatabase:\n  driver: sqlite\n  sqlite_path: ./data/x.db\n"+
			"security:\n  provider_master_key: \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"\n"+
			"update:\n  github_repo: \"\"\n"), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("expected empty github_repo to be accepted, got error: %v", err)
	}
}

// TestGatewayTimeoutsDefaults drives a config with NO `gateway:` block through
// Load: the strict decoder leaves every gateway field at its zero value, and
// applyGatewayDefaults must then fill the idle-keepalive defaults so an upgrade
// without config changes picks up the new timeout model automatically. Reuses
// the chdir-into-empty-tmpdir pattern from TestLoadGeneratesDefaultConfigWhenMissing.
func TestGatewayTimeoutsDefaults(t *testing.T) {
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
		t.Fatalf("Load: %v", err)
	}
	if cfg.Gateway.ConnectTimeout != 5*time.Second {
		t.Errorf("ConnectTimeout default = %v, want 5s", cfg.Gateway.ConnectTimeout)
	}
	if cfg.Gateway.HeaderTimeout != 600*time.Second {
		t.Errorf("HeaderTimeout default = %v, want 600s", cfg.Gateway.HeaderTimeout)
	}
	if cfg.Gateway.FirstByteTimeout != 600*time.Second {
		t.Errorf("FirstByteTimeout default = %v, want 600s", cfg.Gateway.FirstByteTimeout)
	}
	if cfg.Gateway.BodyIdleTimeout != 60*time.Second {
		t.Errorf("BodyIdleTimeout default = %v, want 60s", cfg.Gateway.BodyIdleTimeout)
	}
	if cfg.Gateway.AttemptTimeout != 20*time.Minute {
		t.Errorf("AttemptTimeout default = %v, want 20m", cfg.Gateway.AttemptTimeout)
	}
	if cfg.Gateway.RequestTimeout != 30*time.Minute {
		t.Errorf("RequestTimeout default = %v, want 30m", cfg.Gateway.RequestTimeout)
	}
	if cfg.Gateway.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("TLSHandshakeTimeout default = %v, want 10s", cfg.Gateway.TLSHandshakeTimeout)
	}
}

// TestGenerateDefaultConfigWritesRealGatewayTimeouts pins the requirement that
// generateDefaultConfig must write the real idle-keepalive gateway defaults
// (5s/600s/60s/20m/30m + 10s TLS) to disk, not five 0s. Previously defaults()
// omitted the Gateway block entirely, so the first-run file landed on disk
// with zero values that diverged from both the actual runtime behaviour and
// configs/config.example.yaml.
func TestGenerateDefaultConfigWritesRealGatewayTimeouts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Drive the exact write path: defaults() → marshal → atomicWriteConfig.
	// generateDefaultConfig also creates directories and re-reads, which is
	// covered by TestLoadGeneratesDefaultConfigWhenMissing; here the focus is
	// the ON-DISK content.
	cfg := defaults()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal defaults: %v", err)
	}

	var roundTrip Config
	if err := yaml.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal marshalled defaults: %v", err)
	}

	want := DefaultGatewayConfig()
	if roundTrip.Gateway != want {
		t.Errorf("generated gateway block = %+v, want %+v (real defaults, not 0s)", roundTrip.Gateway, want)
	}

	// Also assert the literal values so a future change to DefaultGatewayConfig
	// that accidentally zeroes a field is caught here too.
	if roundTrip.Gateway.ConnectTimeout != 5*time.Second {
		t.Errorf("ConnectTimeout in generated file = %v, want 5s", roundTrip.Gateway.ConnectTimeout)
	}
	if roundTrip.Gateway.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("TLSHandshakeTimeout in generated file = %v, want 10s", roundTrip.Gateway.TLSHandshakeTimeout)
	}

	// Belt-and-suspenders: write the file and re-load it through the real
	// Load path, confirming the gateway values survive the full round trip.
	// Fill a valid provider_master_key so validate() accepts the file.
	cfg.Security.ProviderMasterKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	data, err = yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("re-marshal defaults with key: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load generated file: %v", err)
	}
	if loaded.Gateway != want {
		t.Errorf("loaded gateway = %+v, want %+v", loaded.Gateway, want)
	}
}

// TestGatewayTimeoutsValidation drives every layering-invariant violation
// through validateGatewayTimeouts individually. Every field must be strictly
// positive (a zero/negative would make a timer fire immediately or disable a
// dial timeout). The only ordering constraints enforced are the ones that
// reflect a real same-attempt nesting relationship: header_timeout <=
// attempt_timeout, first_byte_timeout <= attempt_timeout, and attempt_timeout
// < request_timeout. connect_timeout and body_idle_timeout bound independent
// phases (dial, and inter-chunk body gaps) and are deliberately NOT ordered
// against each other or against header_timeout — a connect_timeout larger
// than body_idle_timeout (e.g. a slow network with a tight steady-state gap)
// is a valid deployment choice, not a misconfiguration. The "header ==
// attempt (equal allowed)" case pins that the `<=` rule accepts equality — a
// future refactor flipping it to strict `<` would flip that case to wantErr.
func TestGatewayTimeoutsValidation(t *testing.T) {
	valid := GatewayConfig{
		ConnectTimeout:      5 * time.Second,
		HeaderTimeout:       600 * time.Second,
		FirstByteTimeout:    600 * time.Second,
		BodyIdleTimeout:     60 * time.Second,
		AttemptTimeout:      20 * time.Minute,
		RequestTimeout:      30 * time.Minute,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	cases := []struct {
		name    string
		mutate  func(*GatewayConfig)
		wantErr bool
	}{
		{"valid defaults", nil, false},
		{"zero connect", func(g *GatewayConfig) { g.ConnectTimeout = 0 }, true},
		{"zero tls_handshake", func(g *GatewayConfig) { g.TLSHandshakeTimeout = 0 }, true},
		{"zero first_byte", func(g *GatewayConfig) { g.FirstByteTimeout = 0 }, true},
		// connect_timeout and body_idle_timeout bound independent phases —
		// a large dial budget with a tight inter-chunk idle budget (or vice
		// versa) must be accepted, not rejected.
		{"connect > body_idle (independent phases, must be accepted)", func(g *GatewayConfig) { g.ConnectTimeout = 60 * time.Second }, false},
		{"body_idle > header (independent phases, must be accepted)", func(g *GatewayConfig) { g.HeaderTimeout = 60 * time.Second }, false},
		{"header > attempt", func(g *GatewayConfig) { g.HeaderTimeout = 25 * time.Minute }, true},
		{"header == attempt (equal allowed)", func(g *GatewayConfig) { g.HeaderTimeout = 20 * time.Minute }, false},
		{"first_byte > attempt", func(g *GatewayConfig) { g.FirstByteTimeout = 25 * time.Minute }, true},
		{"first_byte == attempt (equal allowed)", func(g *GatewayConfig) { g.FirstByteTimeout = 20 * time.Minute }, false},
		{"attempt >= request", func(g *GatewayConfig) { g.AttemptTimeout = 30 * time.Minute }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := valid
			if tc.mutate != nil {
				tc.mutate(&g)
			}
			err := validateGatewayTimeouts(&g)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected nil, got %v", err)
			}
		})
	}
}

// TestLoadGatewayRejectsExplicitZeroTimeout pins the behavior that explicit
// zero timeouts are rejected by validateGatewayTimeouts: with
// applyGatewayDefaults no longer running inside loadStrict, an explicit
// `0s` in the file is no longer silently papered over with the default — it
// reaches validateGatewayTimeouts as 0 and fails the `> 0` check. The user
// gets an error pointing at the bad field instead of a config that quietly
// runs with the default while looking like it honors the override.
func TestLoadGatewayRejectsExplicitZeroTimeout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "server:\n  port: 8080\n" +
		"database:\n  driver: sqlite\n  sqlite_path: ./data/x.db\n" +
		"security:\n  provider_master_key: \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"\n" +
		"gateway:\n  request_timeout: 0s\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error for gateway.request_timeout: 0s; applyGatewayDefaults must NOT silently default-load it")
	}
}

// TestLoadGatewayPartialBlockKeepsDefaultsForOmittedFields pins the behaviour
// loadStrict relies on: defaults() seeds every gateway field, the yaml
// decoder only overwrites fields the user set, so a partial `gateway:` block
// ends up with explicit values where the user wrote them and default values
// where they didn't — no applyGatewayDefaults pass needed.
func TestLoadGatewayPartialBlockKeepsDefaultsForOmittedFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Set only request_timeout; every other gateway field is omitted and
	// must come back as the built-in default.
	content := "server:\n  port: 8080\n" +
		"database:\n  driver: sqlite\n  sqlite_path: ./data/x.db\n" +
		"security:\n  provider_master_key: \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"\n" +
		"gateway:\n  request_timeout: 45m\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The one explicit field round-trips.
	if cfg.Gateway.RequestTimeout != 45*time.Minute {
		t.Errorf("RequestTimeout = %v, want 45m (explicit value)", cfg.Gateway.RequestTimeout)
	}
	// Omitted fields keep their defaults().
	want := DefaultGatewayConfig()
	if cfg.Gateway.ConnectTimeout != want.ConnectTimeout {
		t.Errorf("ConnectTimeout = %v, want default %v", cfg.Gateway.ConnectTimeout, want.ConnectTimeout)
	}
	if cfg.Gateway.HeaderTimeout != want.HeaderTimeout {
		t.Errorf("HeaderTimeout = %v, want default %v", cfg.Gateway.HeaderTimeout, want.HeaderTimeout)
	}
	if cfg.Gateway.FirstByteTimeout != want.FirstByteTimeout {
		t.Errorf("FirstByteTimeout = %v, want default %v", cfg.Gateway.FirstByteTimeout, want.FirstByteTimeout)
	}
	if cfg.Gateway.BodyIdleTimeout != want.BodyIdleTimeout {
		t.Errorf("BodyIdleTimeout = %v, want default %v", cfg.Gateway.BodyIdleTimeout, want.BodyIdleTimeout)
	}
	if cfg.Gateway.AttemptTimeout != want.AttemptTimeout {
		t.Errorf("AttemptTimeout = %v, want default %v", cfg.Gateway.AttemptTimeout, want.AttemptTimeout)
	}
	if cfg.Gateway.TLSHandshakeTimeout != want.TLSHandshakeTimeout {
		t.Errorf("TLSHandshakeTimeout = %v, want default %v", cfg.Gateway.TLSHandshakeTimeout, want.TLSHandshakeTimeout)
	}
}
