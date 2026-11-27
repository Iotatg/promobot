package ai

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Media kind constants (mirroring media_analyzer.py).
const (
	MediaPhoto    = "photo"
	MediaVideo    = "video"
	MediaAudio    = "audio"
	MediaDocument = "document"
)

// size limits, mirroring media_analyzer.py.
const (
	photoMax = 5 * 1024 * 1024
	videoMax = 20 * 1024 * 1024
	otherMax = 15 * 1024 * 1024
)

var (
	imageMimes = map[string]bool{"image/jpeg": true, "image/jpg": true, "image/png": true, "image/webp": true, "image/gif": true}
	imageExts  = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true}

	videoMimes = map[string]bool{"video/mp4": true, "video/mpeg": true, "video/quicktime": true, "video/webm": true, "video/x-matroska": true}
	videoExts  = map[string]bool{".mp4": true, ".mov": true, ".mkv": true, ".webm": true, ".mpeg": true, ".mpg": true, ".avi": true, ".m4v": true}

	audioFormats = map[string]string{
		"audio/wav": "wav", "audio/x-wav": "wav", "audio/wave": "wav",
		"audio/mpeg": "mp3", "audio/mp3": "mp3",
		"audio/ogg": "ogg", "audio/opus": "ogg", "audio/webm": "webm",
		"audio/mp4": "mp3", "audio/m4a": "mp3", "audio/x-m4a": "mp3",
	}
	audioExts = map[string]string{".wav": "wav", ".mp3": "mp3", ".ogg": "ogg", ".opus": "ogg", ".m4a": "mp3"}

	textMimes = map[string]bool{
		"text/plain": true, "text/markdown": true, "text/html": true, "text/css": true,
		"text/javascript": true, "text/xml": true, "text/csv": true, "text/yaml": true,
		"text/x-python": true, "text/x-go": true, "text/x-java": true, "text/x-c": true,
		"application/json": true, "application/javascript": true, "application/xml": true,
		"application/x-yaml": true, "application/x-sh": true, "application/x-python": true,
		"application/x-httpd-php": true, "application/toml": true,
	}
	textExts = map[string]bool{
		".md": true, ".txt": true, ".py": true, ".html": true, ".htm": true, ".css": true,
		".js": true, ".json": true, ".csv": true, ".xml": true, ".yaml": true, ".yml": true,
		".sh": true, ".toml": true, ".ini": true, ".cfg": true, ".conf": true, ".sql": true,
		".go": true, ".rs": true, ".java": true, ".c": true, ".cpp": true, ".h": true, ".rb": true,
		".php": true, ".ts": true, ".tsx": true, ".jsx": true, ".vue": true, ".kt": true,
		".swift": true, ".dart": true, ".lua": true, ".pl": true, ".scala": true, ".cs": true,
		".tex": true, ".proto": true, ".gradle": true, ".log": true, ".env": true, ".r": true,
	}
)

// MediaInput is a piece of media to analyze.
type MediaInput struct {
	Kind     string
	MIME     string
	Filename string
	Data     []byte
	// Context is an optional recent-conversation transcript so the model can
	// answer follow-ups about the media with continuity.
	Context string
}

// blindReply matches AI replies that pretend the media was not received or not
// understood (mirrors media_analyzer.py's _BLIND_RE, extended to catch audio
// evasions like "samajh nahi aaya / unclear / sunai nahi / phir se bol"). It is
// only applied to media answers, so a bare "samajh nahi" always means the
// analysis failed.
var blindReply = regexp.MustCompile(`(?i)(attach nahi|nahi hui|nahi hua|nahi hai yahan|nahi mili|nahi mila|nahi aayi|nahi aaya|blank dikh|kuch nahi dikh|kuch attach nahi|kuch nahi aaya|pdf khul nahi|khul nahi raha|khul hi nahi|koi (video|file|audio|image|photo|attachment) nahi|file attach nahi|video nahi|audio nahi|voice nahi|samajh nahi|samjha nahi|samajh na|sunai nahi|sunai na|suna nahi|suni nahi|sun nahi|nahi sunai|nahi suna|theek se suna|sahi se suna|theek se sunai|awaaz nahi|awaz nahi|awaaz clear nahi|awaz clear nahi|audio clear nahi|voice clear nahi|kya bola|kya kaha|phir se bol|phir se bolo|phir bolo|phir bol|dobara bol|dobara bolo|ek baar phir|wapas bol|audio (unclear|garbled|broken|not clear|distorted)|voice (unclear|garbled|broken|not clear|distorted)|unclear|not clear|clear nahi|don't see|do not see|nothing attached|no (video|file|audio|image|attachment)|can't (see|open|play|read|hear|understand)|cannot (see|open|play|read|hear|understand)|not attached|didn't (get|receive|see|hear|catch)|did not (get|receive|see|hear|catch)|unable to (see|open|play|read|hear|understand)|couldn't (see|open|play|read|hear|understand|catch|make out)|cannot make out|not audible|inaudible|didn't get the audio|no audio|say (that |it )?again|send (it )?again|come again|\brepeat\b)`)

// thinkRe strips <think>...</think> reasoning blocks some models emit.
var thinkRe = regexp.MustCompile(`(?is)<think>.*?</think>`)

// EmptyCaption is used when a media message has no caption (mirrors
// media_analyzer.py's EMPTY_CAPTION).
const EmptyCaption = "look at this"

func stripThinking(s string) string {
	return strings.TrimSpace(thinkRe.ReplaceAllString(s, ""))
}

// MediaSystem builds the instruction used for media analysis, using the active
// persona system prompt as a base when present.
func (c *Client) mediaInstruction() string {
	base := "You are chatting on Telegram."
	if strings.TrimSpace(c.system) != "" {
		base = c.system
	}
	return base + " The user just shared a photo, video, audio or file. " +
		"If it is a voice note or audio, actually listen and understand what is said, then react to it. " +
		"If it is a document, code or archive, read/summarize the useful content inside it. " +
		"Reply in 1-2 short, messy lowercase Hinglish lines about the actual media you see/hear/read. " +
		"Never say you are an AI. No lists, no English essay. Never claim you cannot see, hear or open the media."
}

// AnalyzeMedia downloads already done by the caller: it sends the media bytes to
// the multimodal endpoint and returns the model's short reply.
func (c *Client) AnalyzeMedia(ctx context.Context, caption string, in MediaInput) (string, error) {
	if c.url == "" {
		return "", fmt.Errorf("chat url is not configured")
	}
	if overSize(in, int64(len(in.Data))) {
		return "", ErrMediaTooBig
	}

	primary, err := buildPart(in)
	if err != nil {
		return "", err
	}
	parts := partsToTry(primary)
	models := c.mediaModelList()
	text := c.mediaInstruction()
	if name := strings.TrimSpace(in.Filename); name != "" {
		text += "\n\nAttached file: " + name
		if in.MIME != "" {
			text += " (" + in.MIME + ")"
		}
	}
	if ctxText := strings.TrimSpace(in.Context); ctxText != "" {
		text += "\n\nRecent chat context (for continuity):\n" + ctxText
	}
	if cap := strings.TrimSpace(caption); cap != "" {
		text += "\n\n" + sanitizeArtifacts(cap)
	} else {
		text += "\n\n" + EmptyCaption
	}

	var lastErr error
	for _, model := range models {
		for _, part := range parts {
			reply, err := c.postMedia(ctx, model, text, part)
			if err != nil {
				lastErr = err
				continue
			}
			reply = sanitizeArtifacts(stripThinking(reply))
			if reply == "" || blindReply.MatchString(reply) {
				lastErr = fmt.Errorf("empty or blind reply")
				continue
			}
			return reply, nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no reply")
	}
	return "", lastErr
}

func (c *Client) mediaModelList() []string {
	if len(c.mediaModels) > 0 {
		return c.mediaModels
	}
	if c.model == "" {
		return []string{"free/gemini-4-hydrogen"}
	}
	return []string{c.model}
}

// SetMediaModels sets the ordered list of models to try for media analysis.
func (c *Client) SetMediaModels(models []string) {
	c.mediaModels = models
}

func (c *Client) postMedia(ctx context.Context, model, text string, part map[string]any) (string, error) {
	payload := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": text},
					part,
				},
			},
		},
		"max_tokens":  4096,
		"temperature": 0.45,
		"thinking":    map[string]any{"type": "disabled"},
		"chat_template_kwargs": map[string]any{
			"enable_thinking": false,
		},
	}
	body, _ := json.Marshal(payload)

	reqCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := c.currentKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("llm media status %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return extractModelText(out)
}

func extractModelText(body []byte) (string, error) {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			Text string `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode llm media response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("llm returned no choices")
	}
	ch := parsed.Choices[0]
	if len(ch.Message.Content) > 0 {
		if s := contentToString(ch.Message.Content); s != "" {
			return stripThinking(s), nil
		}
	}
	return stripThinking(ch.Text), nil
}

func contentToString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var items []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &items); err == nil {
		var b strings.Builder
		for _, it := range items {
			b.WriteString(it.Text)
		}
		return strings.TrimSpace(b.String())
	}
	return ""
}

func overSize(in MediaInput, size int64) bool {
	return size > SizeLimit(in.Kind, in.MIME, in.Filename)
}

// ErrMediaTooBig is returned when media exceeds the per-kind size limit.
var ErrMediaTooBig = errors.New("media too large")

// SizeLimit returns the byte limit for a media kind (mirrors media_analyzer.py).
func SizeLimit(kind, mime, filename string) int64 {
	switch {
	case kind == MediaPhoto || (kind == MediaDocument && isImage(mime, filename)):
		return photoMax
	case kind == MediaVideo || (kind == MediaDocument && isVideo(mime, filename)):
		return videoMax
	default:
		return otherMax
	}
}

// buildPart converts media bytes into the primary content part for the API.
func buildPart(in MediaInput) (map[string]any, error) {
	if isTextBased(in.MIME, in.Filename) {
		if txt := readTextContent(in.Data); txt != "" {
			head := "[text file"
			if name := strings.TrimSpace(in.Filename); name != "" {
				head += ": " + name
			}
			head += "]"
			return map[string]any{"type": "text", "text": head + "\n" + txt}, nil
		}
	}
	if isPDF(in.MIME, in.Filename) {
		if txt := extractPDFText(in.Data); txt != "" {
			return map[string]any{"type": "text", "text": "[pdf]\n" + txt}, nil
		}
	}
	if in.Kind == MediaDocument {
		if digest, ok := documentDigest(in); ok {
			return map[string]any{"type": "text", "text": digest}, nil
		}
	}

	b64 := base64.StdEncoding.EncodeToString(in.Data)
	switch {
	case in.Kind == MediaPhoto || (in.Kind == MediaDocument && isImage(in.MIME, in.Filename)):
		mime := in.MIME
		if mime == "" {
			mime = "image/jpeg"
		}
		if mime == "image/jpg" {
			mime = "image/jpeg"
		}
		return map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + mime + ";base64," + b64}}, nil
	case in.Kind == MediaVideo || (in.Kind == MediaDocument && isVideo(in.MIME, in.Filename)):
		mime := in.MIME
		if mime == "" {
			mime = "video/mp4"
		}
		return map[string]any{"type": "video_url", "video_url": map[string]any{"url": "data:" + mime + ";base64," + b64}}, nil
	case in.Kind == MediaAudio || (in.Kind == MediaDocument && audioFormat(in.MIME, in.Filename) != ""):
		// Prefer the true container/codec in a file part (OGG/Opus voice notes get
		// garbled when mislabeled as wav/mp3); partsToTry adds an input_audio
		// fallback for providers that only accept that shape.
		fmtName := audioFormat(in.MIME, in.Filename)
		mime := in.MIME
		if mime == "" {
			mime = audioMimeForFormat(fmtName)
		}
		file := map[string]any{"file_data": "data:" + mime + ";base64," + b64}
		if name := strings.TrimSpace(in.Filename); name != "" {
			file["filename"] = name
		} else {
			file["filename"] = "voice." + audioExtForFormat(fmtName)
		}
		return map[string]any{"type": "file", "file": file}, nil
	default:
		mime := in.MIME
		if mime == "" {
			mime = "application/octet-stream"
		}
		file := map[string]any{"file_data": "data:" + mime + ";base64," + b64}
		if in.Filename != "" {
			file["filename"] = in.Filename
		}
		return map[string]any{"type": "file", "file": file}, nil
	}
}

func audioExtForFormat(format string) string {
	switch format {
	case "mp3":
		return "mp3"
	case "wav":
		return "wav"
	case "ogg":
		return "ogg"
	case "webm":
		return "webm"
	default:
		return "ogg"
	}
}

func audioMimeForFormat(format string) string {
	switch format {
	case "mp3":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "webm":
		return "audio/webm"
	default:
		return "audio/ogg"
	}
}

// partsToTry returns the primary part plus provider-specific fallbacks.
func partsToTry(part map[string]any) []map[string]any {
	switch part["type"] {
	case "video_url":
		return []map[string]any{part, toFilePart(part, "video.mp4")}
	case "input_audio":
		return []map[string]any{part, toFilePart(part, "voice.ogg")}
	case "file":
		if f, ok := part["file"].(map[string]any); ok {
			data, _ := f["file_data"].(string)
			name, _ := f["filename"].(string)
			if strings.Contains(data, "audio") || audioExtMatch(name) {
				return []map[string]any{part, toInputAudioPart(part)}
			}
		}
		return []map[string]any{part}
	default:
		return []map[string]any{part}
	}
}

func audioExtMatch(name string) bool {
	_, ok := audioExts[strings.ToLower(filepath.Ext(name))]
	return ok
}

// toInputAudioPart converts a file part carrying audio into an input_audio part.
func toInputAudioPart(part map[string]any) map[string]any {
	block, _ := part["file"].(map[string]any)
	data, _ := block["file_data"].(string)
	name, _ := block["filename"].(string)
	mime := "audio/ogg"
	if strings.HasPrefix(data, "data:") {
		if i := strings.Index(data, ";base64,"); i >= 0 {
			mime = strings.TrimPrefix(data[:i], "data:")
			data = data[i+len(";base64,"):]
		}
	}
	format := audioFormat(mime, name)
	if format == "" {
		format = "ogg"
	}
	return map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": data, "format": format}}
}

func toFilePart(part map[string]any, filename string) map[string]any {
	url := ""
	switch part["type"] {
	case "video_url":
		if b, ok := part["video_url"].(map[string]any); ok {
			url, _ = b["url"].(string)
		}
	case "image_url":
		if b, ok := part["image_url"].(map[string]any); ok {
			url, _ = b["url"].(string)
		}
	case "input_audio":
		if b, ok := part["input_audio"].(map[string]any); ok {
			if data, ok := b["data"].(string); ok {
				f, _ := b["format"].(string)
				if f == "" {
					f = "ogg"
				}
				url = "data:audio/" + f + ";base64," + data
			}
		}
	}
	file := map[string]any{"file_data": url}
	if filename != "" {
		file["filename"] = filename
	}
	return map[string]any{"type": "file", "file": file}
}

func isImage(mime, filename string) bool {
	if mime != "" && imageMimes[strings.ToLower(strings.Split(mime, ";")[0])] {
		return true
	}
	return imageExts[strings.ToLower(filepath.Ext(filename))]
}

func isVideo(mime, filename string) bool {
	if mime != "" && videoMimes[strings.ToLower(strings.Split(mime, ";")[0])] {
		return true
	}
	return videoExts[strings.ToLower(filepath.Ext(filename))]
}

func audioFormat(mime, filename string) string {
	if mime != "" {
		key := strings.ToLower(strings.Split(mime, ";")[0])
		if f, ok := audioFormats[key]; ok {
			return f
		}
		if f, ok := audioFormats[strings.ToLower(mime)]; ok {
			return f
		}
	}
	if f, ok := audioExts[strings.ToLower(filepath.Ext(filename))]; ok {
		return f
	}
	return ""
}

func isTextBased(mime, filename string) bool {
	if mime != "" && textMimes[strings.ToLower(strings.Split(mime, ";")[0])] {
		return true
	}
	return textExts[strings.ToLower(filepath.Ext(filename))]
}

func isPDF(mime, filename string) bool {
	if mime != "" && strings.ToLower(strings.Split(mime, ";")[0]) == "application/pdf" {
		return true
	}
	return strings.ToLower(filepath.Ext(filename)) == ".pdf"
}

func readTextContent(data []byte) string {
	if len(data) > 50000 {
		data = data[:50000]
	}
	return strings.ToValidUTF8(string(data), "")
}

// extractPDFText does a best-effort plain-text extraction from a PDF, mirroring
// media_analyzer.py's stream-literal scan. Returns "" when nothing usable.
func extractPDFText(data []byte) string {
	if !bytes.HasPrefix(data, []byte("%PDF")) {
		return ""
	}
	streamRe := regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	litRe := regexp.MustCompile(`\((?:\\.|[^\\()])*\)`)
	hexRe := regexp.MustCompile(`<([0-9A-Fa-f\s]+)>`)

	var texts []string
	collect := func(blob []byte) {
		for _, lit := range litRe.FindAll(blob, -1) {
			if dec := pdfDecodeLiteral(lit[1 : len(lit)-1]); hasAlpha(dec) {
				texts = append(texts, dec)
			}
		}
		for _, m := range hexRe.FindAllSubmatch(blob, -1) {
			hex := strings.Join(strings.Fields(string(m[1])), "")
			if len(hex)%2 != 0 {
				hex += "0"
			}
			if raw, err := hexDecode(hex); err == nil && hasAlpha(string(raw)) {
				texts = append(texts, string(raw))
			}
		}
	}

	for _, m := range streamRe.FindAllSubmatch(data, -1) {
		blob := m[1]
		collect(blob)
		if zr, err := zlib.NewReader(bytes.NewReader(blob)); err == nil {
			if dec, err := io.ReadAll(io.LimitReader(zr, 1<<20)); err == nil {
				collect(dec)
			}
			zr.Close()
		}
	}
	if len(texts) == 0 {
		collect(data)
	}
	cleaned := strings.Join(strings.Fields(strings.Join(texts, " ")), " ")
	if len(cleaned) > 20000 {
		cleaned = cleaned[:20000]
	}
	return cleaned
}

func pdfDecodeLiteral(raw []byte) string {
	var out []byte
	for i := 0; i < len(raw); {
		if raw[i] == '\\' && i+1 < len(raw) {
			n := raw[i+1]
			switch n {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '(', ')', '\\':
				out = append(out, n)
			default:
				if n >= '0' && n <= '7' {
					j := i + 1
					oct := []byte{}
					for j < len(raw) && len(oct) < 3 && raw[j] >= '0' && raw[j] <= '7' {
						oct = append(oct, raw[j])
						j++
					}
					if v, err := strconv.ParseInt(string(oct), 8, 32); err == nil {
						out = append(out, byte(v))
					}
					i = j
					continue
				}
			}
			i += 2
			continue
		}
		out = append(out, raw[i])
		i++
	}
	return string(out)
}

func hasAlpha(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}

func hexDecode(hexStr string) ([]byte, error) {
	return hex.DecodeString(hexStr)
}
