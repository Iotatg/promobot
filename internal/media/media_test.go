package media

import (
	"testing"

	"github.com/Iotatg/gotdbot"
)

func TestKindFromPath(t *testing.T) {
	cases := map[string]string{
		"a.JPG":  KindPhoto,
		"b.webp": KindPhoto,
		"c.mp4":  KindVideo,
		"d.mp3":  KindAudio,
		"e.gif":  KindAnimation,
		"f.pdf":  KindDocument,
		"no_ext": KindDocument,
		"x.PNG":  KindPhoto,
	}
	for path, want := range cases {
		if got := KindFromPath(path); got != want {
			t.Errorf("KindFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestExtFor(t *testing.T) {
	if got := extFor("/tmp/some.webp", KindSticker); got != ".webp" {
		t.Errorf("extFor preserved ext = %q, want .webp", got)
	}
	cases := map[string]string{
		KindPhoto:     ".jpg",
		KindVideo:     ".mp4",
		KindAudio:     ".mp3",
		KindAnimation: ".gif",
		KindSticker:   ".webp",
		KindDocument:  ".bin",
	}
	for kind, want := range cases {
		if got := extFor("/tmp/noextension", kind); got != want {
			t.Errorf("extFor(_, %q) = %q, want %q", kind, got, want)
		}
	}
}

func TestFromMessageSticker(t *testing.T) {
	msg := &gotdbot.Message{
		Content: &gotdbot.MessageSticker{
			Sticker: &gotdbot.Sticker{
				Emoji: "😀",
				Sticker: &gotdbot.File{
					Remote: &gotdbot.RemoteFile{Id: "remote-sticker-id", UniqueId: "unique"},
				},
			},
		},
	}
	m := FromMessage(msg)
	if m == nil {
		t.Fatal("FromMessage returned nil for a sticker message")
	}
	if m.Kind != KindSticker {
		t.Errorf("Kind = %q, want %q", m.Kind, KindSticker)
	}
	if !m.IsRemote || m.Source != "remote-sticker-id" {
		t.Errorf("got source=%q remote=%v, want remote-sticker-id/true", m.Source, m.IsRemote)
	}
}

func TestFromMessageStickerUniqueFallback(t *testing.T) {
	msg := &gotdbot.Message{
		Content: &gotdbot.MessageSticker{
			Sticker: &gotdbot.Sticker{
				Sticker: &gotdbot.File{
					Remote: &gotdbot.RemoteFile{UniqueId: "only-unique"},
				},
			},
		},
	}
	m := FromMessage(msg)
	if m == nil || m.Source != "only-unique" {
		t.Fatalf("expected unique id fallback, got %+v", m)
	}
}

func TestFromMessagePlainText(t *testing.T) {
	msg := &gotdbot.Message{Content: &gotdbot.MessageText{Text: &gotdbot.FormattedText{Text: "hi"}}}
	if m := FromMessage(msg); m != nil {
		t.Errorf("FromMessage(plain text) = %+v, want nil", m)
	}
}

func TestFromMessageStickerCaptionEmpty(t *testing.T) {
	msg := &gotdbot.Message{
		Content: &gotdbot.MessageSticker{
			Sticker: &gotdbot.Sticker{
				Sticker: &gotdbot.File{Remote: &gotdbot.RemoteFile{Id: "id"}},
			},
		},
	}
	m := FromMessage(msg)
	if m == nil {
		t.Fatal("nil media")
	}
	if m.Caption != "" {
		t.Errorf("sticker caption = %q, want empty", m.Caption)
	}
}
