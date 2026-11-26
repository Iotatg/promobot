package ai

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"promobot/internal/config"
)

// TestAnalyzeAudioLive verifies voice-note understanding (.wav and .ogg/opus)
// end-to-end against the configured endpoint. Skipped unless RUN_LIVE_AUDIO=1
// and both espeak-ng and ffmpeg are available.
func TestAnalyzeAudioLive(t *testing.T) {
	if os.Getenv("RUN_LIVE_AUDIO") != "1" {
		t.Skip("set RUN_LIVE_AUDIO=1 to run the live audio analysis check")
	}
	espeak, err := exec.LookPath("espeak-ng")
	if err != nil {
		t.Skip("espeak-ng not installed")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
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

	dir := t.TempDir()
	wav := filepath.Join(dir, "speech.wav")
	ogg := filepath.Join(dir, "speech.ogg")
	phrase := "Hello. This is a test voice note. The meeting is at five PM tomorrow in the main office."
	if out, err := exec.Command(espeak, "-w", wav, phrase).CombinedOutput(); err != nil {
		t.Fatalf("espeak-ng: %v: %s", err, out)
	}
	if out, err := exec.Command(ffmpeg, "-y", "-i", wav, "-c:a", "libopus", "-b:a", "24k", ogg).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}

	client := New(cfg.UserLLMAPIKey, cfg.ResolveChatURL(), cfg.UserLLMModel, "")
	client.SetMediaModels([]string{cfg.UserLLMModel})

	cases := []struct {
		name, mime string
		path       string
	}{
		{"wav", "audio/wav", wav},
		{"ogg-opus", "audio/ogg", ogg},
	}
	for _, tc := range cases {
		data, rerr := os.ReadFile(tc.path)
		if rerr != nil {
			t.Fatalf("read %s: %v", tc.path, rerr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		reply, aerr := client.AnalyzeMedia(ctx, "what did the voice note say? answer briefly", MediaInput{
			Kind:     MediaAudio,
			MIME:     tc.mime,
			Filename: filepath.Base(tc.path),
			Data:     data,
		})
		cancel()
		if aerr != nil {
			t.Errorf("%s: analyze error: %v", tc.name, aerr)
			continue
		}
		t.Logf("%s reply=%q blind=%v", tc.name, reply, blindReply.MatchString(reply))
	}
}
