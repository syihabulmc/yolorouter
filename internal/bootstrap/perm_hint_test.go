package bootstrap

import (
	"strings"
	"testing"
)

// The hint is meant to be pasted verbatim into whatever shell the operator
// happens to be using. A shell-specific variable would expand in only one of
// cmd.exe and PowerShell and silently fail to restrict the file in the other,
// so no variable syntax may survive into the emitted command.
func TestRestrictFileHintContainsNoShellVariable(t *testing.T) {
	hint := restrictFileHint(`C:\yolo\configs\config.yaml`)

	for _, bad := range []string{"%USERNAME%", "$env:", "${env:", "$USER"} {
		if strings.Contains(hint, bad) {
			t.Errorf("hint must not rely on shell expansion, found %q in: %s", bad, hint)
		}
	}
}

// /inheritance:r drops only inherited ACEs and /grant:r replaces only the
// named account's grant, so an explicit ACE for a broad principal would
// survive both and keep the master-key file readable. Removal is asserted by
// SID because icacls matches localized display names ("Jeder" on a German
// Windows), which a name-based /remove would miss.
func TestRestrictFileHintClearsBroadPrincipals(t *testing.T) {
	hint := restrictFileHint(`C:\yolo\configs\config.yaml`)

	for name, sid := range map[string]string{
		"Everyone":            "*S-1-1-0",
		"BUILTIN\\Users":      "*S-1-5-32-545",
		"Authenticated Users": "*S-1-5-11",
	} {
		if !strings.Contains(hint, "/remove:g "+sid) {
			t.Errorf("hint must clear %s by SID (%s), got: %s", name, sid, hint)
		}
	}
	if !strings.Contains(hint, "/inheritance:r") {
		t.Errorf("hint must drop inherited ACEs, got: %s", hint)
	}
}

func TestVerifyFileACLHintShowsTheResultingACL(t *testing.T) {
	path := `C:\yolo\configs\config.yaml`
	got := verifyFileACLHint(path)
	if got != `icacls "`+path+`"` {
		t.Errorf("verify hint should list the file's ACL, got: %s", got)
	}
}

func TestRestrictFileHintQuotesThePathItWasGiven(t *testing.T) {
	path := `C:\Program Files\yolo\configs\config.yaml`
	hint := restrictFileHint(path)

	// Quoting is what keeps a path containing spaces from being split into
	// separate icacls arguments.
	if !strings.Contains(hint, `"`+path+`"`) {
		t.Errorf("hint must contain the quoted path %q, got: %s", path, hint)
	}
	if !strings.HasPrefix(hint, "icacls ") {
		t.Errorf("hint should be a runnable icacls command, got: %s", hint)
	}
	if !strings.HasSuffix(hint, `:F"`) {
		t.Errorf("hint should grant full control to a named account, got: %s", hint)
	}
}
