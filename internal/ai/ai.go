package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultMaxTurns is how many recent messages are kept per session.
const defaultMaxTurns = 24

// artifactTagRe matches stray control/parameter tags such as "</rfl:parameter>"
// that occasionally leak into the prompt or the model reply.
var artifactTagRe = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9_.-]*:[A-Za-z][A-Za-z0-9_.-]*(\s[^<>]*)?/?>`)

// multiSpaceRe collapses runs of spaces/tabs left behind after inline removal.
var multiSpaceRe = regexp.MustCompile(`[ \t]{2,}`)

// sanitizeArtifacts removes stray namespace-style tags from text. Lines that
// consist only of such tags are dropped entirely; inline tags are stripped.
func sanitizeArtifacts(s string) string {
	if s == "" || !artifactTagRe.MatchString(s) {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" || !artifactTagRe.MatchString(ln) {
			out = append(out, ln)
			continue
		}
		cleaned := multiSpaceRe.ReplaceAllString(artifactTagRe.ReplaceAllString(ln, ""), " ")
		if strings.TrimSpace(cleaned) == "" {
			continue
		}
		out = append(out, cleaned)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// robotPhraseRe matches the canned "assistant/bot" filler the persona must
// never use. The model keeps emitting these despite the prompt, so they are
// stripped deterministically from every reply.
var robotPhraseRe = regexp.MustCompile(`(?i)(?:bol\s+kaam\s+ka\s+scene(?:\s+kya\s+hai)?|kaam\s+ka\s+scene(?:\s+kya\s+hai)?|kya\s+scene\s+hai|scene\s+kya\s+hai|kya\s+kaam\s+hai|kaam\s+kya\s+hai|bata\s+na\s+kya\s+chahiye|bata\s+na\s+kya\s+kaam(?:\s+hai)?|ye\s+kya\s+majak\s+hai)`)

var (
	robotCommaRe = regexp.MustCompile(`\s*,\s*(?:,\s*)+`)
	robotSpaceRe = regexp.MustCompile(`[ \t]{2,}`)
	robotPunctRe = regexp.MustCompile(`\s+([,.!?])`)
)

// deRobotize strips the robotic filler phrases and tics the persona forbids,
// then tidies the leftover punctuation so the text still reads naturally.
func deRobotize(s string) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	s = robotPhraseRe.ReplaceAllString(s, "")
	s = capWord(s, "tu bata", 1)
	s = capWord(s, "hehe", 1)
	s = capEmoji(s, "😏", 1)
	s = robotCommaRe.ReplaceAllString(s, ", ")
	s = robotPunctRe.ReplaceAllString(s, "$1")
	s = robotSpaceRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	s = strings.Trim(s, " ,")
	if s == "" {
		return "hmm"
	}
	return s
}

// capWord keeps at most n case-insensitive occurrences of word, dropping the
// rest. Word boundaries keep it from cutting inside other words.
func capWord(s, word string, n int) string {
	re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(word))
	idx := re.FindAllStringIndex(s, -1)
	if len(idx) <= n {
		return s
	}
	var b strings.Builder
	last := 0
	kept := 0
	for _, loc := range idx {
		if kept < n {
			kept++
			continue
		}
		b.WriteString(s[last:loc[0]])
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// capEmoji keeps at most n occurrences of an emoji, dropping the rest.
func capEmoji(s, emoji string, n int) string {
	if strings.Count(s, emoji) <= n {
		return s
	}
	var b strings.Builder
	kept := 0
	for _, r := range s {
		if string(r) == emoji {
			if kept < n {
				kept++
				b.WriteRune(r)
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Client is a minimal OpenAI-compatible chat completions client.
type Client struct {
	apiKey string
	url    string
	model  string
	system string
	http   *http.Client

	mu       sync.Mutex
	sessions map[string][]Turn
	loaded   map[string]bool
	maxTurns int

	// mem, when set, persists conversation memory across restarts. ns
	// namespaces keys so different accounts sharing one store never collide.
	mem Memory
	ns  string

	// mediaModels is an ordered fallback list used for multimodal analysis.
	mediaModels []string
}

// Turn is a single stored conversation message.
type Turn struct {
	Role    string `json:"role" bson:"role"`
	Content string `json:"content" bson:"content"`
}

// Memory persists conversation turns so they survive restarts. It is
// implemented by the store.
type Memory interface {
	LoadMemory(ctx context.Context, key string, max int) ([]Turn, error)
	SaveMemory(ctx context.Context, key string, turns []Turn) error
	ClearMemory(ctx context.Context, key string) error
	// ClearMemoryByPrefix deletes every stored conversation whose key starts
	// with prefix+"|", returning how many were removed.
	ClearMemoryByPrefix(ctx context.Context, prefix string) (int64, error)
}

// New creates an AI client. url is the full chat completions endpoint. system
// is an optional system prompt prepended to every request (e.g. a persona).
func New(apiKey, url, model, system string) *Client {
	if strings.TrimSpace(model) == "" {
		model = "free/gemini-4-hydrogen"
	}
	return &Client{
		apiKey:   strings.TrimSpace(apiKey),
		url:      strings.TrimSpace(url),
		model:    model,
		system:   system,
		http:     &http.Client{Timeout: 90 * time.Second},
		sessions: make(map[string][]Turn),
		loaded:   make(map[string]bool),
		maxTurns: defaultMaxTurns,
	}
}

// SetMemory installs a persistence backend and a namespace prefix used to key
// stored conversations. Keys become "<ns>|<key>".
func (c *Client) SetMemory(m Memory, ns string) {
	c.mu.Lock()
	c.mem = m
	c.ns = ns
	c.mu.Unlock()
}

// Clone returns a fresh client with the same credentials, model, persona and
// media models but its own empty conversation memory. Used to give each
// assistant account an independent, persistent memory.
func (c *Client) Clone() *Client {
	if c == nil {
		return nil
	}
	return &Client{
		apiKey:      c.apiKey,
		url:         c.url,
		model:       c.model,
		system:      c.system,
		http:        &http.Client{Timeout: 90 * time.Second},
		sessions:    make(map[string][]Turn),
		loaded:      make(map[string]bool),
		maxTurns:    c.maxTurns,
		mediaModels: append([]string(nil), c.mediaModels...),
	}
}

// Model returns the active model name.
func (c *Client) Model() string { return c.model }

// SetModel changes the active model.
func (c *Client) SetModel(m string) {
	if strings.TrimSpace(m) != "" {
		c.model = m
	}
}

// SetSystem changes the system prompt used for subsequent requests.
func (c *Client) SetSystem(s string) { c.system = s }

type chatRequest struct {
	Model       string  `json:"model"`
	Messages    []Turn  `json:"messages"`
	Temperature float64 `json:"temperature,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message Turn `json:"message"`
	} `json:"choices"`
}

// Ask sends a single-turn prompt using the active model.
func (c *Client) Ask(ctx context.Context, prompt string) (string, error) {
	return c.ask(ctx, c.model, "", prompt)
}

// AskModel sends a single-turn prompt using a specific model.
func (c *Client) AskModel(ctx context.Context, model, prompt string) (string, error) {
	return c.ask(ctx, model, "", prompt)
}

// AskSession is like Ask but keeps per-key conversation memory so the model
// sees recent turns. An empty key disables memory.
func (c *Client) AskSession(ctx context.Context, key, prompt string) (string, error) {
	return c.ask(ctx, c.model, key, prompt)
}

// ResetSession clears the stored conversation for key (memory and persistence).
func (c *Client) ResetSession(ctx context.Context, key string) {
	id := c.ident(key)
	c.mu.Lock()
	delete(c.sessions, id)
	mem := c.mem
	c.mu.Unlock()
	if mem != nil {
		_ = mem.ClearMemory(ctx, id)
	}
}

// RememberTurn stores a user/assistant exchange in the session for key.
func (c *Client) RememberTurn(ctx context.Context, key, prompt, reply string) {
	c.remember(ctx, key, prompt, reply)
}

// SessionKey returns the memory key for a bot-style chat ID.
func SessionKey(chatID int64) string {
	return "chat:" + strconv.FormatInt(chatID, 10)
}

// RecentContext returns a compact transcript of the last n turns for key, used
// to give stateless media analysis some conversational continuity. Each turn is
// clipped so the transcript stays small.
func (c *Client) RecentContext(ctx context.Context, key string, n int) string {
	if key == "" || n <= 0 {
		return ""
	}
	id := c.ident(key)
	c.ensureHistory(ctx, id)
	c.mu.Lock()
	hist := c.sessions[id]
	if len(hist) > n {
		hist = hist[len(hist)-n:]
	}
	hist = append([]Turn(nil), hist...)
	c.mu.Unlock()

	var b strings.Builder
	for _, t := range hist {
		content := sanitizeArtifacts(strings.TrimSpace(t.Content))
		if content == "" {
			continue
		}
		role := "user"
		if t.Role == "assistant" {
			role = "assistant"
		}
		b.WriteString(role)
		b.WriteString(": ")
		b.WriteString(clipRunes(content, 280))
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// clipRunes truncates s to at most max runes, appending an ellipsis marker.
func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

// ident maps a caller key to its namespaced, persisted identifier.
func (c *Client) ident(key string) string {
	if c.ns == "" {
		return key
	}
	return c.ns + "|" + key
}

// ensureHistory loads a key's stored history into the in-memory cache once.
func (c *Client) ensureHistory(ctx context.Context, id string) {
	c.mu.Lock()
	if c.loaded[id] || c.mem == nil {
		c.mu.Unlock()
		return
	}
	c.loaded[id] = true
	mem := c.mem
	max := c.maxTurns
	c.mu.Unlock()

	turns, err := mem.LoadMemory(ctx, id, max)
	if err != nil || len(turns) == 0 {
		return
	}
	c.mu.Lock()
	if _, ok := c.sessions[id]; !ok {
		c.sessions[id] = turns
	}
	c.mu.Unlock()
}

func (c *Client) ask(ctx context.Context, model, key, prompt string) (string, error) {
	if c.url == "" {
		return "", fmt.Errorf("chat url is not configured")
	}
	if strings.TrimSpace(model) == "" {
		model = c.model
	}
	prompt = sanitizeArtifacts(prompt)

	reply, err := c.rawChat(ctx, chatRequest{
		Model:       model,
		Messages:    c.buildMessages(ctx, key, prompt),
		Temperature: 0.7,
	})
	if err != nil {
		return "", err
	}
	reply = deRobotize(sanitizeArtifacts(reply))
	c.remember(ctx, key, prompt, reply)
	return reply, nil
}

// rawChat marshals and sends a chat request, returning the first choice content.
func (c *Client) rawChat(ctx context.Context, reqBody chatRequest) (string, error) {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("llm status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode llm response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("llm returned no choices")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

// DecideMood asks the model to classify the mood of a chat message into a single
// category (happy, love, funny, angry, sad, shock, hype, cool, sleepy, like or
// none). It is stateless (no persona, no memory) and best-effort; callers map
// the mood to an emoji pool and fall back to the offline engine on error.
func (c *Client) DecideMood(ctx context.Context, text string) (string, error) {
	if c.url == "" {
		return "", fmt.Errorf("chat url is not configured")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "none", nil
	}
	sys := "You classify the mood of a chat message into EXACTLY ONE category from this list: " +
		"happy, love, funny, angry, sad, shock, hype, cool, sleepy, like, none. " +
		"Reply with only that single word, nothing else."
	reply, err := c.rawChat(ctx, chatRequest{
		Model: c.model,
		Messages: []Turn{
			{Role: "system", Content: sys},
			{Role: "user", Content: clipRunes(text, 300)},
		},
		Temperature: 0.2,
	})
	if err != nil {
		return "", err
	}
	return normalizeMood(reply), nil
}

// normalizeMood extracts the first recognized mood word from reply, defaulting
// to "none".
func normalizeMood(reply string) string {
	low := strings.ToLower(reply)
	for _, m := range []string{"happy", "love", "funny", "angry", "sad", "shock", "hype", "cool", "sleepy", "like", "none"} {
		if strings.Contains(low, m) {
			return m
		}
	}
	return "none"
}

// ClearAllMemory clears the in-memory cache and every persisted conversation for
// this client's namespace, returning how many stored docs were removed.
func (c *Client) ClearAllMemory(ctx context.Context) (int64, error) {
	c.mu.Lock()
	c.sessions = make(map[string][]Turn)
	c.loaded = make(map[string]bool)
	mem := c.mem
	ns := c.ns
	c.mu.Unlock()
	if mem == nil || ns == "" {
		return 0, nil
	}
	return mem.ClearMemoryByPrefix(ctx, ns)
}

// buildMessages prepends the system prompt (with the current IST time) and any
// stored conversation history for key.
func (c *Client) buildMessages(ctx context.Context, key, prompt string) []Turn {
	msgs := make([]Turn, 0, 4)
	if strings.TrimSpace(c.system) != "" {
		sys := c.system + "\n\nCURRENT TIME (IST): " + istNow() +
			"\n(Use this for date/time questions. Never say you don't know the date.)"
		msgs = append(msgs, Turn{Role: "system", Content: sys})
	}
	if key != "" {
		id := c.ident(key)
		c.ensureHistory(ctx, id)
		c.mu.Lock()
		if hist := c.sessions[id]; len(hist) > 0 {
			msgs = append(msgs, hist...)
		}
		c.mu.Unlock()
	}
	msgs = append(msgs, Turn{Role: "user", Content: prompt})
	return msgs
}

// remember appends a user/assistant turn to the session, trimming to maxTurns,
// and persists the updated history when a memory backend is configured.
func (c *Client) remember(ctx context.Context, key, prompt, reply string) {
	if key == "" || strings.TrimSpace(reply) == "" {
		return
	}
	id := c.ident(key)
	c.mu.Lock()
	hist := append(c.sessions[id],
		Turn{Role: "user", Content: prompt},
		Turn{Role: "assistant", Content: reply},
	)
	if c.maxTurns > 0 && len(hist) > c.maxTurns {
		hist = hist[len(hist)-c.maxTurns:]
	}
	c.sessions[id] = hist
	mem := c.mem
	c.mu.Unlock()

	if mem != nil {
		_ = mem.SaveMemory(ctx, id, hist)
	}
}

// istNow returns the current time formatted in India Standard Time (UTC+5:30).
func istNow() string {
	loc := time.FixedZone("IST", 5*3600+30*60)
	return time.Now().In(loc).Format("Mon, 02 Jan 2006, 03:04 PM")
}

// SplitText chunks text into pieces of at most size runes for safe Telegram
// sends.
func SplitText(text string, size int) []string {
	if size <= 0 {
		size = 4000
	}
	runes := []rune(text)
	var out []string
	for len(runes) > 0 {
		n := size
		if n > len(runes) {
			n = len(runes)
		}
		out = append(out, string(runes[:n]))
		runes = runes[n:]
	}
	return out
}
