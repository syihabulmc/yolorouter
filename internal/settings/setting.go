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

// VisionFallbackSetting is the typed snapshot of the global vision-fallback
// configuration, read as a whole for the same torn-read reason. An empty
// Model means the feature is off; an empty Prompt means "use
// VisionFallbackDefaultPrompt".
type VisionFallbackSetting struct {
	Model  string
	Prompt string
}

// VisionFallbackDefaultPrompt is the runtime fallback describe instruction,
// used only when the stored prompt is empty (never configured, or cleared on
// purpose). It is NOT what the console displays: the console prefills its
// own localized default texts from the frontend i18n bundle and saves them
// as real content, so this constant and those texts are separate artifacts
// that may evolve independently.
const VisionFallbackDefaultPrompt = "Describe this image in detail. Be specific about text, diagrams, charts, code, or any visual content that would be useful for a language model to understand."
