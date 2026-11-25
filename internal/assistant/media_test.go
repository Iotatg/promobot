package assistant

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestMessageMediaKind(t *testing.T) {
	photo := &tg.Message{Media: &tg.MessageMediaPhoto{Photo: &tg.Photo{}}}
	if k, ok := messageMediaKind(photo); !ok || k != "photo" {
		t.Errorf("photo kind = %q, %v", k, ok)
	}

	stickerDoc := &tg.Document{Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeSticker{}}}
	sticker := &tg.Message{Media: &tg.MessageMediaDocument{Document: stickerDoc}}
	if _, ok := messageMediaKind(sticker); ok {
		t.Error("sticker should be excluded from media kinds")
	}

	nothing := &tg.Message{}
	if _, ok := messageMediaKind(nothing); ok {
		t.Error("empty message should have no media kind")
	}
}

func TestDocKind(t *testing.T) {
	cases := []struct {
		name string
		doc  *tg.Document
		want string
	}{
		{"voice", &tg.Document{MimeType: "audio/ogg", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Voice: true}}}, "audio"},
		{"video", &tg.Document{MimeType: "video/mp4", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{}}}, "video"},
		{"gif", &tg.Document{MimeType: "video/mp4", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeAnimated{}}}, "video"},
		{"sticker", &tg.Document{MimeType: "image/webp", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeSticker{}}}, ""},
		{"image by mime", &tg.Document{MimeType: "image/png"}, "photo"},
		{"document by mime", &tg.Document{MimeType: "application/zip"}, "document"},
	}
	for _, tc := range cases {
		if got := docKind(tc.doc); got != tc.want {
			t.Errorf("%s: docKind = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestLargestPhotoSize(t *testing.T) {
	photo := &tg.Photo{Sizes: []tg.PhotoSizeClass{
		&tg.PhotoSize{Type: "s", Size: 100},
		&tg.PhotoSize{Type: "x", Size: 5000},
		&tg.PhotoSizeProgressive{Type: "y", Sizes: []int{1000, 9000}},
	}}
	typ, size, ok := largestPhotoSize(photo)
	if !ok || typ != "y" || size != 9000 {
		t.Errorf("largestPhotoSize = %q, %d, %v; want y, 9000, true", typ, size, ok)
	}
	if _, _, ok := largestPhotoSize(&tg.Photo{}); ok {
		t.Error("empty photo should report no size")
	}
}

func TestDocFilename(t *testing.T) {
	doc := &tg.Document{Attributes: []tg.DocumentAttributeClass{
		&tg.DocumentAttributeAudio{},
		&tg.DocumentAttributeFilename{FileName: "report.pdf"},
	}}
	if got := docFilename(doc); got != "report.pdf" {
		t.Errorf("docFilename = %q, want report.pdf", got)
	}
}
