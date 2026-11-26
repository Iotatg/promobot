package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"promobot/internal/config"
)

func TestAudioProbe(t *testing.T) {
	if os.Getenv("RUN_LIVE_AUDIO_PROBE") != "1" {
		t.Skip("set RUN_LIVE_AUDIO_PROBE=1")
	}
	espeak, _ := exec.LookPath("espeak-ng")
	ffmpeg, _ := exec.LookPath("ffmpeg")
	if espeak == "" || ffmpeg == "" {
		t.Skip("need espeak-ng and ffmpeg")
	}
	if root := findModuleRoot(); root != "" {
		_ = os.Chdir(root)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	dir := t.TempDir()
	wav := filepath.Join(dir, "s.wav")
	ogg := filepath.Join(dir, "s.ogg")
	_ = exec.Command(espeak, "-w", wav, "Hello, the meeting is at five PM tomorrow.").Run()
	_ = exec.Command(ffmpeg, "-y", "-i", wav, "-c:a", "libopus", ogg).Run()
	oggB, _ := os.ReadFile(ogg)

	models := []string{
		"free/minimax-m3", "free/mimo-v2.6-pro", "free/muse-spark-1.3",
		"free/qwen-3.8-max", "gpt-6.1-terra", "gpt-6.2-astra", "free/minimax-m3.1-flash",
	}
	part := map[string]any{"type": "file", "file": map[string]any{"file_data": "data:audio/ogg;base64," + b64(oggB), "filename": "voice.ogg"}}
	for i, model := range models {
		if i > 0 {
			time.Sleep(5 * time.Second)
		}
		body := map[string]any{
			"model": model,
			"messages": []map[string]any{{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "Transcribe this voice note in one line."},
					part,
				},
			}},
		}
		status, resp := postRaw(cfg.ResolveChatURL(), cfg.UserLLMAPIKey, body)
		t.Logf("%-26s status=%d resp=%.180s", model, status, contentOf(resp))
	}
}

func contentOf(resp string) string {
	var m map[string]any
	if json.Unmarshal([]byte(resp), &m) != nil {
		return resp
	}
	chs, _ := m["choices"].([]any)
	if len(chs) == 0 {
		return resp
	}
	c0, _ := chs[0].(map[string]any)
	msg, _ := c0["message"].(map[string]any)
	s, _ := msg["content"].(string)
	return s
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func postRaw(url, key string, body map[string]any) (int, string) {
	payload, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, string(b)
}
