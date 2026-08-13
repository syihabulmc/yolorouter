package visionfallback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/yolorouter/yolorouter/internal/loopback"
	"github.com/yolorouter/yolorouter/pkg/logger"
)

// describeAll fetches a text description for each attempted image
// concurrently through the gateway's own API. The result slice is
// index-aligned with sites; "" means that image got no description (not
// attempted, or its call failed) and the caller decides what stands in for
// it. Failures are per-image on purpose: one unreachable URL must not cost
// the other nine descriptions. Goroutines write disjoint indices and Wait
// precedes every read, so the slice needs no lock.
func (f *Fallback) describeAll(ctx context.Context, sites []imageSite, attempted []bool, prompt string, view View) []string {
	out := make([]string, len(sites))
	var wg sync.WaitGroup
	for i, site := range sites {
		if !attempted[i] {
			continue
		}
		wg.Add(1)
		go func(i int, url string) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					logger.Error("vision fallback: describe panicked",
						zap.String("request_id", view.RequestID()), zap.Any("panic", r))
				}
			}()
			desc, err := f.describeOne(ctx, url, prompt, view)
			if err != nil {
				logger.Warn("vision fallback: image describe failed",
					zap.String("request_id", view.RequestID()), zap.Int("image", i), zap.Error(err))
				return
			}
			out[i] = desc
		}(i, site.url)
	}
	wg.Wait()
	return out
}

// describeOne makes one loopback chat-completions call: the configured
// vision model, one image, the describe prompt. The caller's own credential
// authenticates it, so the description's cost lands on the key that sent the
// image; the internal token keeps the self-call from re-entering this
// capability, and the parent header lets its audit row point back.
func (f *Fallback) describeOne(ctx context.Context, imageURL, prompt string, view View) (string, error) {
	reqBody := map[string]any{
		"model": view.VisionFallbackModel(),
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "image_url", "image_url": map[string]string{"url": imageURL}},
					{"type": "text", "text": prompt},
				},
			},
		},
		"max_tokens": 1024,
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal describe request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.loopbackBase+"/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", fmt.Errorf("build describe request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+view.AuthCredential())
	req.Header.Set(loopback.HeaderInternal, loopback.Token)
	req.Header.Set(loopback.HeaderParent, view.RequestID())

	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("describe call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read describe response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := string(raw)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return "", fmt.Errorf("describe status %d: %s", resp.StatusCode, snippet)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("parse describe response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("describe response has no choices")
	}
	desc := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if desc == "" {
		return "", fmt.Errorf("describe response is empty")
	}
	return desc, nil
}
