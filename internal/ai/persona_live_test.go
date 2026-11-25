package ai

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"promobot/internal/config"
	"promobot/internal/persona"
)

// TestPersonaLive exercises the real LLM endpoint with the Radhika persona.
// It is skipped unless RUN_LIVE_LLM=1 so it never runs in normal test runs.
func TestPersonaLive(t *testing.T) {
	if os.Getenv("RUN_LIVE_LLM") != "1" {
		t.Skip("set RUN_LIVE_LLM=1 to run the live persona check")
	}

	// Reuse the project's .env (loaded relative to the module root).
	if root := findModuleRoot(); root != "" {
		if err := os.Chdir(root); err != nil {
			t.Fatalf("chdir module root: %v", err)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	sys := persona.Config{
		Name:  cfg.PersonaName,
		Age:   cfg.PersonaAge,
		DOB:   cfg.PersonaDOB,
		From:  cfg.PersonaFrom,
		Owner: "meri creator",
	}.System()

	client := New(cfg.UserLLMAPIKey, cfg.ResolveChatURL(), cfg.UserLLMModel, sys)

	resp, err := client.Ask(context.Background(), "Hi, who are you? Tell me your name, age and date of birth in one short line.")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	t.Logf("model response: %s", resp)

	if !strings.Contains(strings.ToLower(resp), "radhika") {
		t.Fatalf("expected persona name 'Radhika' in reply, got: %s", resp)
	}
}

// TestMemoryLive checks that the assistant remembers earlier turns, using the
// live LLM. Skipped unless RUN_LIVE_LLM=1.
func TestMemoryLive(t *testing.T) {
	if os.Getenv("RUN_LIVE_LLM") != "1" {
		t.Skip("set RUN_LIVE_LLM=1 to run the live memory check")
	}
	if root := findModuleRoot(); root != "" {
		_ = os.Chdir(root)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	c := New(cfg.UserLLMAPIKey, cfg.ResolveChatURL(), cfg.UserLLMModel, "")
	ctx := context.Background()

	if _, err := c.AskSession(ctx, "live:mem", "My favourite color is teal. Just acknowledge briefly."); err != nil {
		t.Fatalf("first ask: %v", err)
	}
	resp, err := c.AskSession(ctx, "live:mem", "What is my favourite color? Answer with just the color.")
	if err != nil {
		t.Fatalf("second ask: %v", err)
	}
	t.Logf("memory recall: %s", resp)
	if !strings.Contains(strings.ToLower(resp), "teal") {
		t.Fatalf("expected memory recall of 'teal', got: %s", resp)
	}
}

func findModuleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
