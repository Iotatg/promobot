package ai

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestZipDigestReadsInnerText(t *testing.T) {
	data := makeZip(t, map[string]string{
		"main.py":   "print('hello from zip')\n",
		"README.md": "# Title\nsome notes\n",
		"img.bin":   "\x00\x01\x02",
	})
	in := MediaInput{Kind: MediaDocument, MIME: "application/zip", Filename: "code.zip", Data: data}
	digest, ok := documentDigest(in)
	if !ok {
		t.Fatal("expected zip digest")
	}
	if !strings.Contains(digest, "main.py") || !strings.Contains(digest, "hello from zip") {
		t.Errorf("digest missing inner python content: %q", digest)
	}
	if !strings.Contains(digest, "README.md") || !strings.Contains(digest, "some notes") {
		t.Errorf("digest missing inner markdown content: %q", digest)
	}
}

func TestOfficeDigest(t *testing.T) {
	data := makeZip(t, map[string]string{
		"word/document.xml": `<w:document><w:body><w:p><w:t>Hello Docx World</w:t></w:p></w:body></w:document>`,
	})
	in := MediaInput{Kind: MediaDocument, Filename: "a.docx", Data: data}
	digest, ok := documentDigest(in)
	if !ok || !strings.Contains(digest, "Hello Docx World") {
		t.Fatalf("office digest = %q ok=%v, want Hello Docx World", digest, ok)
	}
}

func TestCodeFileBecomesText(t *testing.T) {
	in := MediaInput{Kind: MediaDocument, MIME: "text/x-python", Filename: "main.py", Data: []byte("print('hi')")}
	part, err := buildPart(in)
	if err != nil {
		t.Fatalf("buildPart: %v", err)
	}
	if part["type"] != "text" {
		t.Fatalf("part type = %v, want text", part["type"])
	}
	text, _ := part["text"].(string)
	if !strings.Contains(text, "main.py") || !strings.Contains(text, "print('hi')") {
		t.Errorf("text part = %q, want filename and code", text)
	}
}

func TestUnknownBinaryGetsMetadata(t *testing.T) {
	in := MediaInput{Kind: MediaDocument, MIME: "application/octet-stream", Filename: "blob.dat", Data: []byte("SOME readable text here \x00\x01 end")}
	digest, ok := documentDigest(in)
	if !ok {
		t.Fatal("expected metadata digest")
	}
	if !strings.Contains(digest, "blob.dat") || !strings.Contains(digest, "SOME readable text") {
		t.Errorf("digest = %q, want metadata and printable preview", digest)
	}
}

func TestAudioNotDigested(t *testing.T) {
	in := MediaInput{Kind: MediaAudio, MIME: "audio/ogg", Filename: "voice.ogg", Data: []byte("OggS....")}
	if _, ok := documentDigest(in); ok {
		t.Error("audio must not be turned into a document digest")
	}
}

func TestRecentContext(t *testing.T) {
	c := New("", "http://example.invalid", "m", "sys")
	ctx := context.Background()
	c.remember(ctx, "chat:1", "hello there", "hi back")
	got := c.RecentContext(ctx, "chat:1", 8)
	if !strings.Contains(got, "user: hello there") || !strings.Contains(got, "assistant: hi back") {
		t.Errorf("RecentContext = %q", got)
	}
	if other := c.RecentContext(ctx, "chat:2", 8); other != "" {
		t.Errorf("RecentContext for other chat = %q, want empty", other)
	}
}
