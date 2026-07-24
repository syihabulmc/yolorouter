// Package settings holds neutral DTO types shared across layers without
// pulling implementation dependencies (gateway imports this for its
// SettingsProvider interface; service imports it to implement that interface).
package settings

// CustomSystemPromptSetting is the typed snapshot of the global custom system
// prompt. Read and published as a whole to avoid torn reads between the
// enabled and text rows. It intentionally has NO json tags — it is an internal
// transfer type; handlers wrap it in their own response DTO with json tags.
type CustomSystemPromptSetting struct {
	Enabled bool
	Text    string
}
