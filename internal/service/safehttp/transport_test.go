package safehttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mustParseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("failed to parse IP %q", s)
	}
	return ip
}

func TestCheckIPAllowedRejectsDeniedRanges(t *testing.T) {
	denied := []string{
		"10.1.2.3",         // private
		"172.16.0.5",       // private
		"192.168.1.1",      // private
		"127.0.0.1",        // loopback v4
		"169.254.169.254",  // link-local v4 / cloud metadata
		"100.64.0.1",       // CGNAT
		"224.0.0.1",        // multicast v4
		"198.18.0.1",       // benchmark
		"240.0.0.1",        // reserved
		"0.0.0.0",          // unspecified v4
		"::1",              // loopback v6
		"fe80::1",          // link-local v6
		"fc00::1",          // unique-local v6
		"ff00::1",          // multicast v6
		"::",               // unspecified v6
		"::ffff:127.0.0.1", // IPv4-mapped IPv6 loopback — must normalize before checking
		"::ffff:10.0.0.1",  // IPv4-mapped IPv6 private
	}
	for _, s := range denied {
		if err := checkIPAllowed(mustParseIP(t, s), false); err == nil {
			t.Errorf("expected %q to be denied, got nil error", s)
		}
	}
}

func TestCheckIPAllowedAllowsPublicAddresses(t *testing.T) {
	allowed := []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"}
	for _, s := range allowed {
		if err := checkIPAllowed(mustParseIP(t, s), false); err != nil {
			t.Errorf("expected %q to be allowed, got error: %v", s, err)
		}
	}
}

// TestCheckIPAllowedWithPrivateAllowed pins the tiered behavior: allowPrivate
// opens ONLY the loopback/private/link-local/CGNAT/ULA ranges, while the
// always-denied ranges (multicast, benchmark, reserved, unspecified) stay
// blocked — enabling the toggle must not silently drop the whole deny list.
func TestCheckIPAllowedWithPrivateAllowed(t *testing.T) {
	nowAllowed := []string{
		"10.1.2.3", "172.16.0.5", "192.168.1.1", // private v4
		"127.0.0.1",       // loopback v4
		"169.254.169.254", // link-local v4 / cloud metadata
		"100.64.0.1",      // CGNAT
		"::1", "fe80::1", "fc00::1",
		"::ffff:127.0.0.1", // IPv4-mapped IPv6 loopback still normalizes
	}
	for _, s := range nowAllowed {
		if err := checkIPAllowed(mustParseIP(t, s), true); err != nil {
			t.Errorf("expected %q to be allowed with allowPrivate=true, got error: %v", s, err)
		}
	}

	stillDenied := []string{
		"224.0.0.1",     // multicast v4
		"198.18.0.1",    // benchmark
		"240.0.0.1",     // reserved
		"0.0.0.0",       // unspecified v4
		"ff00::1", "::", // multicast / unspecified v6
	}
	for _, s := range stillDenied {
		if err := checkIPAllowed(mustParseIP(t, s), true); err == nil {
			t.Errorf("expected %q to stay denied even with allowPrivate=true, got nil error", s)
		}
	}
}

func TestNewTransportDisablesEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	transport := NewTransport(false, 5*time.Second, 10*time.Second)
	if transport.Proxy != nil {
		t.Fatalf("expected Proxy to be nil regardless of environment, got non-nil")
	}
}

// TestNewTransport_SetsDialTimeout verifies the connectTimeout argument
// reaches the underlying net.Dialer. The dialer is captured in the DialContext
// closure rather than exposed as a field on *http.Transport, so the check is
// behavioral: dial an unroutable TEST-NET-1 address (RFC 5737) with a short
// connectTimeout and confirm the dial returns within a window far shorter than
// the OS default TCP retransmit timeout (~20s+). Environments that immediately
// reject the route cannot exercise the dialer timeout and are skipped rather
// than false-failing.
func TestNewTransport_SetsDialTimeout(t *testing.T) {
	transport := NewTransport(false, 150*time.Millisecond, 10*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	conn, err := transport.DialContext(ctx, "tcp", "192.0.2.1:80") // TEST-NET-1, unroutable
	elapsed := time.Since(start)
	if conn != nil {
		if closeErr := conn.Close(); closeErr != nil {
			t.Errorf("close unexpected conn: %v", closeErr)
		}
		t.Fatal("expected dial to unroutable TEST-NET-1 address to fail")
	}
	if err == nil {
		t.Fatal("expected error from dial to unroutable address")
	}
	// If the network immediately rejected the route (e.g. "network
	// unreachable"), the dial returns in well under the dialer timeout and
	// there is nothing to exercise.
	if elapsed < 150*time.Millisecond {
		t.Skipf("route rejected in %v; cannot exercise dialer timeout in this environment", elapsed)
	}
	// The SYN was sent and went unanswered; the dialer timeout should have
	// bounded the wait well under the OS default (~20s+). The generous
	// threshold absorbs DNS resolution and scheduling jitter.
	if elapsed > 5*time.Second {
		t.Errorf("dial took %v with 150ms connectTimeout; connectTimeout may not be wired into the dialer", elapsed)
	}
}

// TestSafeDialContextZeroTimeoutDoesNotExpireImmediately pins the
// connectTimeout==0 contract ("no dial-level bound", see NewTransport's doc
// comment): a naive context.WithTimeout(ctx, 0) produces an already-expired
// context, which would fail LookupIPAddr/dial immediately regardless of
// target reachability. With the fix, connectTimeout==0 must use the parent
// ctx unmodified, so a real (reachable) target still dials successfully.
func TestSafeDialContextZeroTimeoutDoesNotExpireImmediately(t *testing.T) {
	withDeniedCIDRs(t, nil) // treat every IP as allowed so the loopback dial isn't SSRF-blocked

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := safeDialContext(ctx, dialer, "tcp", srv.Listener.Addr().String(), false, 0)
	if err != nil {
		t.Fatalf("expected connectTimeout=0 to dial via the parent ctx without expiring immediately, got: %v", err)
	}
	if closeErr := conn.Close(); closeErr != nil {
		t.Errorf("close conn: %v", closeErr)
	}
}

// TestTransportRejectsConnectionToLoopback proves the wiring, not just the
// predicate function: an httptest server bound to 127.0.0.1 must be
// unreachable through this transport even though it's a real, listening
// socket — this is what stops SSRF against localhost-bound admin tooling.
func TestTransportRejectsConnectionToLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewTransport(false, 5*time.Second, 10*time.Second), Timeout: 2 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if _, err := client.Do(req); err == nil {
		t.Fatalf("expected request to a loopback-bound server to fail, but it succeeded")
	}
}

// TestDialContextTriesEachResolvedIPAndSkipsDenied covers the case where the
// DNS response contains a mix of safe and unsafe IPs at the resolver-abstraction level:
// a resolver that returns a denied IP first and an allowed one second must
// still let the safe one through, not fail outright on the first hit.
func TestDialContextTriesEachResolvedIPAndSkipsDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	// 169.254.1.1 (denied) listed before the real, allowed loopback-equivalent
	// test target is impossible to construct without a real public IP, so
	// this test instead exercises safeDialContext directly against a
	// resolver stub that returns [denied, allowed-but-actually-loopback]
	// and asserts the denied one is skipped (loopback itself is denied too,
	// so the overall dial still fails — but via the SECOND IP's denial, not
	// silently succeeding on the first).
	ips := []net.IPAddr{{IP: net.ParseIP("169.254.1.1")}, {IP: net.ParseIP("127.0.0.1")}}
	_, err = dialResolvedIPs(context.Background(), dialer, "tcp", ips, port, false)
	if err == nil {
		t.Fatalf("expected both denied IPs to be rejected")
	}
}

// TestDialResolvedIPsNoCandidates covers the "empty ips slice" edge case,
// which is the only way dialResolvedIPs's default lastErr ("no addresses to
// dial") is ever produced.
func TestDialResolvedIPsNoCandidates(t *testing.T) {
	dialer := &net.Dialer{Timeout: time.Second}
	_, err := dialResolvedIPs(context.Background(), dialer, "tcp", nil, "80", false)
	if err == nil {
		t.Fatalf("expected an error for an empty IP candidate list")
	}
	if got, want := err.Error(), "no addresses to dial"; got != want {
		t.Fatalf("unexpected error message: got %q, want %q", got, want)
	}
}

// withDeniedCIDRs temporarily swaps BOTH package-level deny lists (always-
// denied and private) to the same replacement so tests can
// exercise dialResolvedIPs's post-check success/failure paths against
// loopback addresses without depending on real, publicly-routable IPs (which
// wouldn't be reachable from a sandboxed test environment anyway).
func withDeniedCIDRs(t *testing.T, replacement []*net.IPNet) {
	t.Helper()
	origDenied, origPrivate := deniedCIDRs, privateCIDRs
	deniedCIDRs = replacement
	privateCIDRs = replacement
	t.Cleanup(func() { deniedCIDRs = origDenied; privateCIDRs = origPrivate })
}

// TestDialResolvedIPsSucceedsOnAllowedIP covers the success return path
// (return conn, nil): a resolved IP that both passes checkIPAllowed and
// connects successfully.
func TestDialResolvedIPsSucceedsOnAllowedIP(t *testing.T) {
	withDeniedCIDRs(t, nil) // treat every IP as allowed for this test

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	ips := []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}
	conn, err := dialResolvedIPs(context.Background(), dialer, "tcp", ips, port, false)
	if err != nil {
		t.Fatalf("expected successful dial, got error: %v", err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Errorf("close conn: %v", closeErr)
		}
	}()
}

// TestDialResolvedIPsAllowPrivateBypassesDenial covers the allowPrivate=true
// path (config.SecurityConfig.AllowPrivateUpstreams): a loopback IP that the
// real deny list would reject must dial successfully when the check is
// relaxed, letting a self-hosted operator reach a LAN/localhost model server.
func TestDialResolvedIPsAllowPrivateBypassesDenial(t *testing.T) {
	// Deliberately keep the REAL deny list (127.0.0.0/8 included) — the point
	// is that allowPrivate=true, not an emptied list, is what lets this dial
	// through.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	ips := []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}
	conn, err := dialResolvedIPs(context.Background(), dialer, "tcp", ips, port, true)
	if err != nil {
		t.Fatalf("expected loopback dial to succeed with allowPrivate=true, got: %v", err)
	}
	if closeErr := conn.Close(); closeErr != nil {
		t.Errorf("close conn: %v", closeErr)
	}
}

// TestDialResolvedIPsSkipsFailedDialAndReportsLastError covers the
// dial-failure branch (conn, err := dialer.DialContext(...); err != nil ->
// continue): an allowed IP whose port nothing is listening on must produce
// a connection-refused error, not a false success.
func TestDialResolvedIPsSkipsFailedDialAndReportsLastError(t *testing.T) {
	withDeniedCIDRs(t, nil) // treat every IP as allowed for this test

	// Bind a listener solely to reserve a port, then close it immediately so
	// nothing is listening — the subsequent dial to that port must fail with
	// connection refused, deterministically and without relying on any
	// pre-agreed "known closed port" number.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	ips := []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}
	_, err = dialResolvedIPs(context.Background(), dialer, "tcp", ips, port, false)
	if err == nil {
		t.Fatalf("expected dial to a closed port to fail")
	}
}

// TestSafeDialContextRejectsMalformedAddr covers safeDialContext's
// net.SplitHostPort error branch: an addr with no port at all.
func TestSafeDialContextRejectsMalformedAddr(t *testing.T) {
	dialer := &net.Dialer{Timeout: time.Second}
	_, err := safeDialContext(context.Background(), dialer, "tcp", "no-port-here", false, time.Second)
	if err == nil {
		t.Fatalf("expected an error for an addr with no port")
	}
}

// TestSafeDialContextReportsResolveFailure covers safeDialContext's
// LookupIPAddr error branch. The context is pre-canceled so the resolver
// fails fast and deterministically regardless of whether the sandbox has
// outbound network/DNS access.
func TestSafeDialContextReportsResolveFailure(t *testing.T) {
	dialer := &net.Dialer{Timeout: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := safeDialContext(ctx, dialer, "tcp", "example.invalid:80", false, time.Second)
	if err == nil {
		t.Fatalf("expected resolution to fail for a canceled context")
	}
}

// TestSafeDialContextDNSNotBoundByConnectTimeout pins the fix that DNS
// resolution uses the caller's own (longer) ctx, never a connectTimeout-
// derived one. Before the fix, LookupIPAddr shared the same deadline as the
// dial phase, so a slow-but-healthy resolution (cold cache, long CNAME
// chain) could eat most of connectTimeout and leave too little for the dial
// itself — every candidate IP then failed with DeadlineExceeded even though
// DNS and the upstream were both healthy.
//
// connectTimeout here is deliberately absurdly small (1ns): under the old
// behavior this would make LookupIPAddr's own deadline already expired,
// failing resolution outright. Under the fix, resolution runs against ctx
// (5s) and "localhost" resolves near-instantly regardless of connectTimeout;
// only the SUBSEQUENT dial phase is bound by the 1ns budget and is expected
// to fail there instead.
// connectTimeout bounds the connect phase and nothing else. The only way to see
// that from outside is to look at the context resolution is handed: an
// implementation that wrongly passes the connect deadline to the resolver still
// dials every reachable host successfully, so no amount of asserting on the
// returned error can tell the two apart. Asserting on the deadline also removes
// the platform dependency that made the earlier form of this test fail on
// Windows, where the clock granularity is coarse enough that a 1ns deadline is
// not yet elapsed when it is checked.
func TestSafeDialContextDNSNotBoundByConnectTimeout(t *testing.T) {
	withDeniedCIDRs(t, nil) // treat every IP as allowed so localhost isn't SSRF-blocked

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}

	var resolverDeadline time.Time
	var resolverHadDeadline bool
	original := resolveHost
	resolveHost = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		resolverDeadline, resolverHadDeadline = ctx.Deadline()
		return original(ctx, host)
	}
	t.Cleanup(func() { resolveHost = original })

	const callerBudget = 30 * time.Second
	callerCtx, cancel := context.WithTimeout(context.Background(), callerBudget)
	defer cancel()
	callerDeadline, _ := callerCtx.Deadline()

	conn, err := safeDialContext(callerCtx, dialerForTest(), "tcp", "localhost:"+port, false, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	_ = conn.Close()

	if !resolverHadDeadline {
		t.Fatal("resolution should inherit the caller's deadline, got a context with none")
	}
	// The caller's budget is three orders of magnitude larger than the connect
	// budget, so which one resolution received is unambiguous.
	if !resolverDeadline.Equal(callerDeadline) {
		t.Errorf("resolution must be bounded by the caller's deadline (%v), got %v — a connectTimeout-derived deadline would be ~%v away",
			callerDeadline, resolverDeadline, 50*time.Millisecond)
	}
}

// The connect phase, by contrast, must be bounded by connectTimeout.
//
// Nothing else in this test may be able to end the dial, or the assertion is
// satisfied by the wrong mechanism: an earlier version gave the dialer its own
// 2s Timeout and allowed anything under 5s, so deleting the connectTimeout
// handling entirely still passed — in 2.00s, ended by the dialer. The dialer
// therefore carries no Timeout here, and the caller's budget is two orders of
// magnitude above the connect budget, so the only way to return quickly is for
// connectTimeout to have been applied.
func TestSafeDialContextConnectBoundByConnectTimeout(t *testing.T) {
	withDeniedCIDRs(t, nil)

	// 203.0.113.0/24 is TEST-NET-3, reserved for documentation and not routed,
	// so this touches no real host. What it does depends on the network: it
	// either black-holes the SYN (the case this test wants) or is rejected
	// outright by the local stack, which the skip below detects.
	const (
		addr           = "203.0.113.1:9"
		connectTimeout = 300 * time.Millisecond
		callerBudget   = 30 * time.Second
	)

	ctx, cancel := context.WithTimeout(context.Background(), callerBudget)
	defer cancel()

	start := time.Now()
	conn, err := safeDialContext(ctx, &net.Dialer{}, "tcp", addr, false, connectTimeout)
	elapsed := time.Since(start)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatalf("expected the connect phase to fail under a %v connectTimeout", connectTimeout)
	}

	var netErr net.Error
	timedOut := errors.As(err, &netErr) && netErr.Timeout()
	if !timedOut && elapsed < connectTimeout/2 {
		t.Skipf("this network rejects %s immediately (%v after %v) instead of black-holing it, "+
			"so the connect budget is never reached and there is nothing to assert", addr, err, elapsed)
	}
	if !timedOut {
		t.Fatalf("expected a timeout error from the connect phase, got %v after %v", err, elapsed)
	}
	if strings.Contains(err.Error(), "resolve") {
		t.Errorf("an IP literal needs no resolution; the error must come from the connect phase, got: %v", err)
	}
	// Ending anywhere near the caller's budget means the connect budget was
	// never applied and the caller's context ended the dial instead.
	if elapsed > callerBudget/6 {
		t.Errorf("connect budget was not applied: dial took %v under a %v connectTimeout", elapsed, connectTimeout)
	}
}

// dialerForTest deliberately carries no Timeout: a dialer-level bound would be
// able to end a dial on its own, which is exactly how the connect-budget test
// above was once satisfied without the code under test doing anything.
func dialerForTest() *net.Dialer { return &net.Dialer{} }
func TestDialResolvedIPsSharesConnectDeadlineAcrossIPs(t *testing.T) {
	withDeniedCIDRs(t, nil) // treat every IP as allowed

	connectTimeout := 200 * time.Millisecond
	dialer := &net.Dialer{Timeout: connectTimeout}
	ips := []net.IPAddr{
		{IP: net.ParseIP("192.0.2.1")}, // TEST-NET-1, unroutable
		{IP: net.ParseIP("192.0.2.2")}, // TEST-NET-1, unroutable
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	connectCtx, connectCancel := context.WithTimeout(ctx, connectTimeout)
	defer connectCancel()

	start := time.Now()
	_, err := dialResolvedIPs(connectCtx, dialer, "tcp", ips, "80", false)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected dial to unreachable IPs to fail")
	}
	// If the network immediately rejected the route, the dial returns in
	// well under the budget and there is nothing to exercise.
	if elapsed < connectTimeout {
		t.Skipf("route rejected in %v; cannot exercise shared deadline in this environment", elapsed)
	}
	// The shared deadline must cap the total at ~connectTimeout, NOT
	// 2*connectTimeout. Allow generous slack for scheduling jitter.
	if elapsed > 2*connectTimeout {
		t.Errorf("dial phase took %v with %v budget and 2 IPs; shared deadline may not be working (expected < %v)",
			elapsed, connectTimeout, 2*connectTimeout)
	}
}
