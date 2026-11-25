package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultMaxTurns is how many recent messages are kept per session.
const defaultMaxTurns = 24

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
		content := strings.TrimSpace(t.Content)
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

	reqBody := chatRequest{
		Model:       model,
		Messages:    c.buildMessages(ctx, key, prompt),
		Temperature: 0.7,
	}
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
	reply := strings.TrimSpace(parsed.Choices[0].Message.Content)
	c.remember(ctx, key, prompt, reply)
	return reply, nil
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
