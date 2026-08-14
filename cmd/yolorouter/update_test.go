package main

import "testing"

func TestBackupCommand(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"empty path emits bare command", "", "  yolorouter db:backup"},
		{"nonempty path is single-quoted", "/etc/ce/prod.yaml", "  yolorouter db:backup --config '/etc/ce/prod.yaml'"},
		{"path with spaces is single-quoted", "/path with spaces/cfg.yaml", "  yolorouter db:backup --config '/path with spaces/cfg.yaml'"},
		// %q (Go double-quote) would let $ expand / backticks execute when an
		// operator pastes the command — single quotes make it literal.
		{"path with $ stays literal", "/etc/$HOME/cfg.yaml", "  yolorouter db:backup --config '/etc/$HOME/cfg.yaml'"},
		{"path with backtick stays literal", "/a/`whoami`/cfg.yaml", "  yolorouter db:backup --config '/a/`whoami`/cfg.yaml'"},
		// Embedded single quote is escaped with the standard '\'' idiom.
		{"path with single quote is escaped", "/it's/cfg.yaml", "  yolorouter db:backup --config '/it'\\''s/cfg.yaml'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := backupCommand(c.path)
			if got != c.want {
				t.Fatalf("backupCommand(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}
