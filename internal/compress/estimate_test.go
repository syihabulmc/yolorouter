package compress

import "testing"

func TestEstimateTokensSaved(t *testing.T) {
	// (400 original chars - 80 compressed chars) / 4 = 80
	if got := estimateTokensSaved(400, 80); got != 80 {
		t.Fatalf("estimateTokensSaved(400,80)=%d, want 80", got)
	}
	// Negative savings clamp to zero (defensive; should not happen normally)
	if got := estimateTokensSaved(80, 400); got != 0 {
		t.Fatalf("estimateTokensSaved(80,400)=%d, want 0", got)
	}
}

func TestShouldAttempt(t *testing.T) {
	if shouldAttempt(511, 512) {
		t.Fatal("511B is below the 512B threshold; should not attempt")
	}
	if !shouldAttempt(512, 512) {
		t.Fatal("512B meets the threshold; should attempt")
	}
}

func TestAcceptCompressed(t *testing.T) {
	if !acceptCompressed("aaaa", "aa") {
		t.Fatal("shorter output should be accepted")
	}
	if acceptCompressed("aa", "aaaa") {
		t.Fatal("longer output should be rejected")
	}
	if acceptCompressed("aa", "aa") {
		t.Fatal("equal-length output should be rejected (no benefit)")
	}
}
