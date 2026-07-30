package bootstrap

import (
	"fmt"
	"os/user"
)

// Well-known SIDs for the broad principals a config file is most likely to be
// exposed through. They are removed by SID rather than by name because the
// display names are localized (a German Windows lists "Jeder" for Everyone),
// and a name-based /remove would silently match nothing there.
const (
	sidEveryone           = "*S-1-1-0"
	sidBuiltinUsers       = "*S-1-5-32-545"
	sidAuthenticatedUsers = "*S-1-5-11"
)

// restrictFileHint builds a ready-to-run icacls command that limits access to
// path to the current account.
//
// The account is resolved here rather than left as a shell variable on
// purpose: %USERNAME% expands in cmd.exe but not in PowerShell (which needs
// ${env:USERNAME}), so a single hardcoded form is silently broken in one of
// the two shells an operator is likely to paste it into — icacls receives the
// unexpanded literal and fails to map it to an account, leaving the file
// unrestricted while looking like the fix was applied. Substituting the real
// account name produces a command that works verbatim in either shell.
//
// /inheritance:r alone is not enough: it drops inherited ACEs, and /grant:r
// only replaces the grant for the account it names, so an explicit ACE
// previously set for a broad principal would survive both and keep the file
// readable. The explicit /remove:g clears the realistic ones. This is still
// not a guaranteed-empty DACL for arbitrary principals — the accompanying
// warning tells the operator to verify the result rather than assuming it.
//
// Only reached on platforms that cannot enforce config permissions (see
// config.PermEnforcementSupported), i.e. Windows, where user.Current returns
// the DOMAIN\user form icacls expects.
func restrictFileHint(path string) string {
	account := "<your-account>"
	if u, err := user.Current(); err == nil && u.Username != "" {
		account = u.Username
	}
	return fmt.Sprintf(`icacls "%s" /inheritance:r /remove:g %s /remove:g %s /remove:g %s /grant:r "%s:F"`,
		path, sidEveryone, sidBuiltinUsers, sidAuthenticatedUsers, account)
}

// verifyFileACLHint is the command that shows the resulting ACL, so the
// operator can confirm only their own account is listed instead of trusting
// that restrictFileHint's command covered every principal.
func verifyFileACLHint(path string) string {
	return fmt.Sprintf(`icacls "%s"`, path)
}
