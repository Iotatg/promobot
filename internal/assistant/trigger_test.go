package assistant

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
)

func TestPersonaTrigger(t *testing.T) {
	c := &Client{selfID: 42}
	c.SetPersona("Radhika")
	c.selfUsername = "radhika_user"

	ctx := context.Background()

	cases := []struct {
		name string
		text string
		want bool
	}{
		{"bare name", "radhika how are you", true},
		{"capitalised", "Hey Radhika!", true},
		{"mid sentence", "so what do you think radhika?", true},
		{"substring not name", "radhikaa is a different word", false},
		{"no name", "hello there everyone", false},
		{"username mention", "@radhika_user ping", true},
		{"name in word", "goradhikax", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &tg.Message{
				PeerID:  &tg.PeerChat{ChatID: 1},
				FromID:  &tg.PeerUser{UserID: 5},
				Message: tc.text,
			}
			if got := c.mentionsOrReplies(ctx, m, tc.text); got != tc.want {
				t.Fatalf("mentionsOrReplies(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestStripTrigger(t *testing.T) {
	c := &Client{}
	c.SetPersona("Radhika")
	c.selfUsername = "radhika_user"

	got := c.stripTrigger("@radhika_user Radhika what's the time?")
	if got != "what's the time?" {
		t.Fatalf("stripTrigger = %q", got)
	}
}

func TestKindFromMime(t *testing.T) {
	cases := map[string]StickerKind{
		"image/webp":              StickerStatic,
		"application/x-tgsticker": StickerAnimated,
		"video/webm":              StickerVideo,
		"image/png":               StickerStatic,
	}
	for mime, want := range cases {
		if got := kindFromMime(mime); got != want {
			t.Fatalf("kindFromMime(%q) = %v, want %v", mime, got, want)
		}
	}
}
