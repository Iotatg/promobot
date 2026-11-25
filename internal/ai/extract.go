package ai

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Extraction limits keep a hostile archive from blowing memory or tokens.
const (
	maxArchiveEntries = 300
	maxEntryBytes     = 64 * 1024
	maxTotalText      = 60 * 1024
	maxDecompressed   = 8 * 1024 * 1024
)

var (
	archiveExts = map[string]bool{
		".zip": true, ".tar": true, ".tgz": true, ".tar.gz": true, ".gz": true,
	}
	archiveMimes = map[string]bool{
		"application/zip": true, "application/x-zip-compressed": true,
		"application/x-tar": true, "application/gzip": true, "application/x-gzip": true,
	}
	officeExts = map[string]bool{
		".docx": true, ".docm": true, ".pptx": true, ".pptm": true,
		".xlsx": true, ".xlsm": true,
	}
	officeMimes = map[string]bool{
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   true,
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": true,
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         true,
	}
)

// documentDigest turns a non-media document into text the model can read. It
// covers archives (listing + inner text), Office Open XML files (docx/pptx/xlsx
// are zips), and arbitrary binaries (metadata + printable preview). It returns
// ok=false for images, videos and audio, which are handled as native parts.
func documentDigest(in MediaInput) (string, bool) {
	if isImage(in.MIME, in.Filename) || isVideo(in.MIME, in.Filename) || audioFormat(in.MIME, in.Filename) != "" {
		return "", false
	}

	header := metadataLine(in)
	switch {
	case isOffice(in.MIME, in.Filename):
		if txt, ok := officeText(in.Data); ok {
			return header + "\n" + txt, true
		}
	case isArchive(in.MIME, in.Filename) || isZipData(in.Data) || isGzipData(in.Data):
		if txt, ok := archiveText(in.Data, in.Filename); ok {
			return header + "\n" + txt, true
		}
	}

	// Unknown or unparsable: give the model metadata plus any printable strings so
	// it can still reason about the file instead of claiming it can't open it.
	if prev := printablePreview(in.Data, 1200); prev != "" {
		return header + "\n[text preview]\n" + prev, true
	}
	return header, true
}

func metadataLine(in MediaInput) string {
	name := strings.TrimSpace(in.Filename)
	if name == "" {
		name = "(unnamed)"
	}
	mime := strings.TrimSpace(in.MIME)
	if mime == "" {
		mime = "application/octet-stream"
	}
	return fmt.Sprintf("[file: %s | type: %s | %d bytes]", name, mime, len(in.Data))
}

func isArchive(mime, filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	if archiveExts[ext] {
		return true
	}
	if strings.HasSuffix(strings.ToLower(filename), ".tar.gz") {
		return true
	}
	if mime != "" && archiveMimes[strings.ToLower(strings.Split(mime, ";")[0])] {
		return true
	}
	return false
}

func isOffice(mime, filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	if officeExts[ext] {
		return true
	}
	if mime != "" && officeMimes[strings.ToLower(strings.Split(mime, ";")[0])] {
		return true
	}
	return false
}

func isZipData(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && (data[2] == 3 || data[2] == 5 || data[2] == 7)
}

func isGzipData(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}

// officeText extracts visible text from docx/pptx/xlsx by reading their inner XML.
func officeText(data []byte) (string, bool) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", false
	}
	var b strings.Builder
	total := 0
	for _, f := range zr.File {
		name := f.Name
		relevant := name == "word/document.xml" ||
			strings.HasPrefix(name, "ppt/slides/slide") ||
			name == "xl/sharedStrings.xml"
		if !relevant {
			continue
		}
		text := xmlToText(readZipEntry(f))
		if strings.TrimSpace(text) == "" {
			continue
		}
		b.WriteString("--- " + name + " ---\n")
		b.WriteString(text)
		b.WriteString("\n")
		total += len(text)
		if total >= maxTotalText {
			break
		}
	}
	if total == 0 {
		return "", false
	}
	return truncate(b.String(), maxTotalText), true
}

// archiveText lists and reads the text content of a zip, tar or gzip archive.
func archiveText(data []byte, filename string) (string, bool) {
	switch {
	case isZipData(data):
		return zipArchiveText(data)
	case isGzipData(data):
		return gzipArchiveText(data)
	case strings.HasSuffix(strings.ToLower(filename), ".tar") || strings.HasSuffix(strings.ToLower(filename), ".tgz"):
		return tarArchiveText(bytes.NewReader(data))
	}
	return "", false
}

func zipArchiveText(data []byte) (string, bool) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", false
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Zip archive with %d entries.\n", len(zr.File)))
	count := 0
	total := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		count++
		b.WriteString(fmt.Sprintf("- %s (%d bytes)\n", f.Name, f.UncompressedSize64))
		if count >= maxArchiveEntries {
			b.WriteString("... (truncated)\n")
			break
		}
		if total >= maxTotalText || !isTextFile(f.Name) {
			continue
		}
		text := readZipEntry(f)
		if len(text) == 0 {
			continue
		}
		if len(text) > maxEntryBytes {
			text = text[:maxEntryBytes]
		}
		b.WriteString("--- " + f.Name + " ---\n")
		b.WriteString(strings.ToValidUTF8(string(text), ""))
		b.WriteString("\n")
		total += len(text)
	}
	return truncate(b.String(), maxTotalText), true
}

func gzipArchiveText(data []byte) (string, bool) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", false
	}
	defer gz.Close()
	raw, err := io.ReadAll(io.LimitReader(gz, maxDecompressed))
	if err != nil || len(raw) == 0 {
		return "", false
	}
	// A gzipped tar still carries .tar contents.
	if txt, ok := tarArchiveText(bytes.NewReader(raw)); ok {
		return txt, true
	}
	return truncate("[gzip: decompressed]\n"+strings.ToValidUTF8(string(raw), ""), maxTotalText), true
}

func tarArchiveText(r io.Reader) (string, bool) {
	tr := tar.NewReader(r)
	var b strings.Builder
	count := 0
	total := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		count++
		b.WriteString(fmt.Sprintf("- %s (%d bytes)\n", hdr.Name, hdr.Size))
		if count >= maxArchiveEntries {
			break
		}
		if total >= maxTotalText || !isTextFile(hdr.Name) {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(tr, maxEntryBytes))
		if len(body) == 0 {
			continue
		}
		b.WriteString("--- " + hdr.Name + " ---\n")
		b.WriteString(strings.ToValidUTF8(string(body), ""))
		b.WriteString("\n")
		total += len(body)
	}
	if count == 0 {
		return "", false
	}
	return truncate(b.String(), maxTotalText), true
}

func readZipEntry(f *zip.File) []byte {
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	b, _ := io.ReadAll(io.LimitReader(rc, maxDecompressed))
	return b
}

func isTextFile(name string) bool {
	return isTextBased("", name)
}

// xmlToText collapses XML markup into its character data.
func xmlToText(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if cd, ok := tok.(xml.CharData); ok {
			b.Write(cd)
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// printablePreview returns a run-length-filtered view of printable ASCII/UTF-8
// text hidden inside a binary blob, useful for formats we cannot parse.
func printablePreview(data []byte, max int) string {
	var b strings.Builder
	run := make([]byte, 0, 64)
	flush := func() {
		if len(run) >= 5 {
			b.Write(run)
			b.WriteByte(' ')
		}
		run = run[:0]
	}
	for _, c := range data {
		if c == '\n' || c == '\r' || c == '\t' || (c >= 32 && c < 127) {
			run = append(run, c)
		} else {
			flush()
		}
		if b.Len() >= max {
			break
		}
	}
	flush()
	out := strings.Join(strings.Fields(b.String()), " ")
	return truncate(out, max)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... (truncated)"
}
