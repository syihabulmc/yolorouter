package gateway

import (
	"context"

	"github.com/yolorouter/yolorouter/internal/settings"
)

// SettingsProvider is the read-only window the gateway has into the cached
// global custom system prompt and input-compression switch. Implemented by
// the system settings service and injected into Service by the router.
// The gateway imports only the neutral settings DTO (not the service
// package), so there is no import cycle.
type SettingsProvider interface {
	CustomSystemPrompt(ctx context.Context) (settings.CustomSystemPromptSetting, int64, error)
	GetInputCompression(ctx context.Context) (bool, int64, error)
	GetVisionFallback(ctx context.Context) (settings.VisionFallbackSetting, int64, error)
}
