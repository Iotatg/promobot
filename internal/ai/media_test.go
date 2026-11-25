package ai

import (
	"strings"
	"testing"
)

func TestOverSize(t *testing.T) {
	cases := []struct {
		name string
		in   MediaInput
		size int64
		want bool
	}{
		{"small photo", MediaInput{Kind: MediaPhoto, MIME: "image/jpeg"}, 4 * 1024 * 1024, false},
		{"big photo", MediaInput{Kind: MediaPhoto, MIME: "image/jpeg"}, 6 * 1024 * 1024, true},
		{"big video", MediaInput{Kind: MediaVideo, MIME: "video/mp4"}, 25 * 1024 * 1024, true},
		{"small video", MediaInput{Kind: MediaVideo, MIME: "video/mp4"}, 10 * 1024 * 1024, false},
		{"image document uses photo limit", MediaInput{Kind: MediaDocument, MIME: "image/png"}, 6 * 1024 * 1024, true},
		{"other document", MediaInput{Kind: MediaDocument, MIME: "application/zip"}, 10 * 1024 * 1024, false},
	}
	for _, tc := range cases {
		if got := overSize(tc.in, tc.size); got != tc.want {
			t.Errorf("%s: overSize = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBuildPartTypes(t *testing.T) {
	cases := []struct {
		name string
		in   MediaInput
		want string
	}{
		{"photo", MediaInput{Kind: MediaPhoto, MIME: "image/jpeg", Data: []byte("x")}, "image_url"},
		{"video", MediaInput{Kind: MediaVideo, MIME: "video/mp4", Data: []byte("x")}, "video_url"},
		{"audio", MediaInput{Kind: MediaAudio, MIME: "audio/ogg", Data: []byte("x")}, "file"},
		{"binary document becomes text", MediaInput{Kind: MediaDocument, MIME: "application/zip", Filename: "a.zip", Data: []byte("x")}, "text"},
		{"text file becomes text", MediaInput{Kind: MediaDocument, MIME: "text/plain", Filename: "a.txt", Data: []byte("hello world")}, "text"},
	}
	for _, tc := range cases {
		part, err := buildPart(tc.in)
		if err != nil {
			t.Fatalf("%s: buildPart error: %v", tc.name, err)
		}
		if part["type"] != tc.want {
			t.Errorf("%s: part type = %v, want %v", tc.name, part["type"], tc.want)
		}
	}
}

func TestPartsToTryFallbacks(t *testing.T) {
	video, _ := buildPart(MediaInput{Kind: MediaVideo, MIME: "video/mp4", Data: []byte("x")})
	if got := partsToTry(video); len(got) != 2 || got[1]["type"] != "file" {
		t.Errorf("video partsToTry = %v, want a file fallback", got)
	}
	audio, _ := buildPart(MediaInput{Kind: MediaAudio, MIME: "audio/ogg", Data: []byte("x")})
	if got := partsToTry(audio); len(got) != 2 || got[1]["type"] != "input_audio" {
		t.Errorf("audio partsToTry = %v, want an input_audio fallback", got)
	}
}

func TestMediaTypeHelpers(t *testing.T) {
	if !isImage("image/jpeg", "") || !isImage("", "x.png") || isImage("application/zip", "a.zip") {
		t.Error("isImage misclassified")
	}
	if !isVideo("video/mp4", "") || !isVideo("", "clip.mkv") || isVideo("image/png", "") {
		t.Error("isVideo misclassified")
	}
	if !isTextBased("text/plain", "") || !isTextBased("", "main.go") || isTextBased("image/png", "") {
		t.Error("isTextBased misclassified")
	}
	if !isTextBased("text/plain; charset=utf-8", "") || !isTextBased("text/x-python", "") {
		t.Error("isTextBased should ignore mime parameters and accept code mimes")
	}
	if !isPDF("application/pdf", "") || !isPDF("", "doc.pdf") || isPDF("text/plain", "") {
		t.Error("isPDF misclassified")
	}
}

func TestAudioFormat(t *testing.T) {
	if got := audioFormat("audio/mpeg", ""); got != "mp3" {
		t.Errorf("audioFormat(mp3) = %q", got)
	}
	if got := audioFormat("", "voice.ogg"); got != "ogg" {
		t.Errorf("audioFormat(.ogg) = %q", got)
	}
	if got := audioFormat("application/zip", "a.zip"); got != "" {
		t.Errorf("audioFormat(zip) = %q, want empty", got)
	}
}

func TestExtractPDFText(t *testing.T) {
	pdf := []byte("%PDF-1.4\n1 0 obj\n<< /Length 24 >>\nstream\nBT (Hello World) Tj ET\nendstream\nendobj\n%%EOF")
	got := extractPDFText(pdf)
	if !strings.Contains(got, "Hello World") {
		t.Errorf("extractPDFText = %q, want it to contain Hello World", got)
	}
	if got := extractPDFText([]byte("not a pdf")); got != "" {
		t.Errorf("extractPDFText(non-pdf) = %q, want empty", got)
	}
}
