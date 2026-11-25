package ai

import (
	"context"
	"encoding/base64"
	"os"
	"testing"
	"time"

	"promobot/internal/config"
)

// TestAnalyzeMediaLive verifies the multimodal AnalyzeMedia path end-to-end
// against the configured endpoint. Skipped unless RUN_LIVE_VISION=1.
func TestAnalyzeMediaLive(t *testing.T) {
	if os.Getenv("RUN_LIVE_VISION") != "1" {
		t.Skip("set RUN_LIVE_VISION=1 to run the live media analysis check")
	}
	if root := findModuleRoot(); root != "" {
		_ = os.Chdir(root)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.ResolveChatURL() == "" || cfg.UserLLMAPIKey == "" {
		t.Skip("llm not configured")
	}

	client := New(cfg.UserLLMAPIKey, cfg.ResolveChatURL(), cfg.UserLLMModel, "")
	client.SetMediaModels([]string{cfg.UserLLMModel})

	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	reply, err := client.AnalyzeMedia(ctx, "what color is this pixel? answer in one word", MediaInput{
		Kind: MediaPhoto,
		MIME: "image/png",
		Data: png,
	})
	if err != nil {
		t.Fatalf("analyze media: %v", err)
	}
	if reply == "" {
		t.Fatal("empty reply")
	}
	t.Logf("reply=%q", reply)
}
