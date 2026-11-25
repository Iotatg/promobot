package assistant

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"promobot/internal/config"
	"promobot/internal/media"
	"promobot/internal/store"
)

// TestKangLive verifies the real Telegram sticker-pack creation flow
// (upload -> create/add) against the live assistant session. It is skipped
// unless RUN_LIVE_KANG=1.
func TestKangLive(t *testing.T) {
	if os.Getenv("RUN_LIVE_KANG") != "1" {
		t.Skip("set RUN_LIVE_KANG=1 to run the live kang check")
	}
	if root := findRoot(); root != "" {
		_ = os.Chdir(root)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.AssistantSession == "" {
		t.Skip("no assistant session configured")
	}

	c, err := FromPyrogramSession(int(cfg.APIID), cfg.APIHash, cfg.AssistantSession, nil)
	if err != nil {
		t.Fatalf("connect assistant: %v", err)
	}
	defer c.Close()

	var buf bytes.Buffer
	if err := png.Encode(&buf, stickerPNG()); err != nil {
		t.Fatalf("encode png: %v", err)
	}

	summary, err := c.KangStickerFile(context.Background(), "sticker.png", buf.Bytes(), "image/png", "😀")
	if err != nil {
		t.Fatalf("kang: %v", err)
	}
	t.Logf("kang summary: %s", summary)
}

// TestSendStickerLive verifies the assistant can send a saved sticker. It is
// skipped unless RUN_LIVE_SEND=1.
func TestSendStickerLive(t *testing.T) {
	if os.Getenv("RUN_LIVE_SEND") != "1" {
		t.Skip("set RUN_LIVE_SEND=1 to run the live sticker send check")
	}
	if root := findRoot(); root != "" {
		_ = os.Chdir(root)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.AssistantSession == "" {
		t.Skip("no assistant session configured")
	}
	c, err := FromPyrogramSession(int(cfg.APIID), cfg.APIHash, cfg.AssistantSession, nil)
	if err != nil {
		t.Fatalf("connect assistant: %v", err)
	}
	defer c.Close()

	doc, err := c.RandomSticker(context.Background(), StickerStatic)
	if err != nil {
		t.Fatalf("random sticker: %v", err)
	}
	if err := c.SendSticker(c.selfID, doc); err != nil {
		t.Fatalf("send sticker: %v", err)
	}
	t.Logf("sent sticker id=%d to saved messages", doc.ID)
}

// TestAutoSaveLive verifies the auto-save path (reusing a received document
// reference and de-duplicating). Skipped unless RUN_LIVE_KANG=1.
func TestAutoSaveLive(t *testing.T) {
	if os.Getenv("RUN_LIVE_KANG") != "1" {
		t.Skip("set RUN_LIVE_KANG=1 to run the live auto-save check")
	}
	if root := findRoot(); root != "" {
		_ = os.Chdir(root)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.AssistantSession == "" {
		t.Skip("no assistant session configured")
	}
	c, err := FromPyrogramSession(int(cfg.APIID), cfg.APIHash, cfg.AssistantSession, nil)
	if err != nil {
		t.Fatalf("connect assistant: %v", err)
	}
	defer c.Close()

	ctx := context.Background()
	doc, err := c.RandomSticker(ctx, StickerStatic)
	if err != nil {
		t.Fatalf("random sticker: %v", err)
	}
	info, err := c.SaveStickerDocument(ctx, doc, "")
	if err != nil {
		t.Fatalf("save sticker: %v", err)
	}
	t.Logf("auto-save: duplicate=%v count=%d pack=%s", info.Duplicate, info.Count, info.ShortName)
	if !info.Duplicate {
		t.Fatalf("expected an existing sticker to be detected as duplicate")
	}
}

// TestSendMediaLive verifies the assistant can upload and send a local media
// file (the path used for scheduled posts and broadcasts). It is skipped unless
// RUN_LIVE_SEND=1.
func TestSendMediaLive(t *testing.T) {
	if os.Getenv("RUN_LIVE_SEND") != "1" {
		t.Skip("set RUN_LIVE_SEND=1 to run the live media send check")
	}
	if root := findRoot(); root != "" {
		_ = os.Chdir(root)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.AssistantSession == "" {
		t.Skip("no assistant session configured")
	}
	c, err := FromPyrogramSession(int(cfg.APIID), cfg.APIHash, cfg.AssistantSession, nil)
	if err != nil {
		t.Fatalf("connect assistant: %v", err)
	}
	defer c.Close()

	path := filepath.Join(t.TempDir(), "photo.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	if err := png.Encode(f, stickerPNG()); err != nil {
		f.Close()
		t.Fatalf("encode png: %v", err)
	}
	f.Close()

	m := store.Media{Kind: media.KindPhoto, Source: path, Caption: "promobot media test"}
	if err := c.SendMedia(c.selfID, m); err != nil {
		t.Fatalf("send media: %v", err)
	}
	t.Logf("sent photo to saved messages from %s", path)
}

func stickerPNG() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 512, 512))
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 200, A: 255})
		}
	}
	return img
}

func findRoot() string {
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
