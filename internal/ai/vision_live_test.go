package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"promobot/internal/config"
)

// TestVisionLive checks whether the configured LLM endpoint accepts a
// multimodal (image) content part. It is skipped unless RUN_LIVE_VISION=1.
func TestVisionLive(t *testing.T) {
	if os.Getenv("RUN_LIVE_VISION") != "1" {
		t.Skip("set RUN_LIVE_VISION=1 to run the live vision check")
	}
	if root := findModuleRoot(); root != "" {
		_ = os.Chdir(root)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	url := cfg.ResolveChatURL()
	if url == "" || cfg.UserLLMAPIKey == "" {
		t.Skip("llm not configured")
	}

	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	payload := map[string]any{
		"model": cfg.UserLLMModel,
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "what color is this pixel? answer in one word"},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + png}},
				},
			},
		},
		"max_tokens": 32,
	}
	body, _ := json.Marshal(payload)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.UserLLMAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	t.Logf("status=%d body=%s", resp.StatusCode, string(out))
	if resp.StatusCode >= 400 {
		t.Fatalf("vision request failed with status %d", resp.StatusCode)
	}
}
