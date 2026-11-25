package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// stubServer records the messages sent to it and returns a fixed reply.
func stubServer(t *testing.T, reply string) (*httptest.Server, func() [][]Turn) {
	t.Helper()
	var mu sync.Mutex
	var captured [][]Turn
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		captured = append(captured, req.Messages)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": reply}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, func() [][]Turn {
		mu.Lock()
		defer mu.Unlock()
		out := make([][]Turn, len(captured))
		copy(out, captured)
		return out
	}
}

func TestSessionMemory(t *testing.T) {
	srv, captured := stubServer(t, "ok")
	c := New("k", srv.URL, "m", "you are a test")
	ctx := context.Background()

	if _, err := c.AskSession(ctx, "chat:1", "first question"); err != nil {
		t.Fatalf("first ask: %v", err)
	}
	if _, err := c.AskSession(ctx, "chat:1", "second question"); err != nil {
		t.Fatalf("second ask: %v", err)
	}
	// Different key must not see history.
	if _, err := c.AskSession(ctx, "chat:2", "other chat"); err != nil {
		t.Fatalf("other ask: %v", err)
	}

	calls := captured()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(calls))
	}
	// Call 2 should contain: system, user first, assistant ok, user second.
	second := calls[1]
	if len(second) != 4 {
		t.Fatalf("expected 4 messages on 2nd call, got %d: %+v", len(second), second)
	}
	if second[0].Role != "system" || second[1].Role != "user" || second[1].Content != "first question" ||
		second[2].Role != "assistant" || second[3].Content != "second question" {
		t.Fatalf("unexpected 2nd call messages: %+v", second)
	}
	// Call 3 (new key) should be system + user only.
	if len(calls[2]) != 2 {
		t.Fatalf("expected 2 messages on 3rd call, got %d: %+v", len(calls[2]), calls[2])
	}

	c.ResetSession(ctx, "chat:1")
	if _, err := c.AskSession(ctx, "chat:1", "after reset"); err != nil {
		t.Fatalf("after reset: %v", err)
	}
	calls = captured()
	after := calls[len(calls)-1]
	if len(after) != 2 {
		t.Fatalf("expected memory cleared (2 messages), got %d: %+v", len(after), after)
	}
}

func TestSessionTrim(t *testing.T) {
	srv, captured := stubServer(t, "ok")
	c := New("k", srv.URL, "m", "")
	c.maxTurns = 4
	ctx := context.Background()
	for i := 0; i < 6; i++ {
		if _, err := c.AskSession(ctx, "k", "msg"); err != nil {
			t.Fatalf("ask %d: %v", i, err)
		}
	}
	calls := captured()
	last := calls[len(calls)-1]
	// system is empty here, so only trimmed history + current user.
	if len(last) > 5 {
		t.Fatalf("history not trimmed: %d messages", len(last))
	}
}

// fakeMem is an in-memory ai.Memory for testing persistence.
type fakeMem struct{ m map[string][]Turn }

func newFakeMem() *fakeMem { return &fakeMem{m: make(map[string][]Turn)} }

func (f *fakeMem) LoadMemory(_ context.Context, key string, max int) ([]Turn, error) {
	t := f.m[key]
	if max > 0 && len(t) > max {
		t = t[len(t)-max:]
	}
	return t, nil
}
func (f *fakeMem) SaveMemory(_ context.Context, key string, turns []Turn) error {
	f.m[key] = turns
	return nil
}
func (f *fakeMem) ClearMemory(_ context.Context, key string) error {
	delete(f.m, key)
	return nil
}

func TestMemoryPersistenceAndIsolation(t *testing.T) {
	srv, captured := stubServer(t, "ok")
	ctx := context.Background()
	fm := newFakeMem()

	a := New("k", srv.URL, "m", "")
	a.SetMemory(fm, "acct1")
	if _, err := a.AskSession(ctx, "chat:9", "q1"); err != nil {
		t.Fatalf("a ask: %v", err)
	}

	// Simulate a restart: a fresh client with the same namespace must reload.
	b := a.Clone()
	b.SetMemory(fm, "acct1")
	if _, err := b.AskSession(ctx, "chat:9", "q2"); err != nil {
		t.Fatalf("b ask: %v", err)
	}
	calls := captured()
	if got := len(calls[len(calls)-1]); got != 3 {
		t.Fatalf("expected reloaded history (3 messages), got %d: %+v", got, calls[len(calls)-1])
	}

	// A different account namespace must not see acct1's history.
	c := New("k", srv.URL, "m", "")
	c.SetMemory(fm, "acct2")
	if _, err := c.AskSession(ctx, "chat:9", "x"); err != nil {
		t.Fatalf("c ask: %v", err)
	}
	calls = captured()
	if got := len(calls[len(calls)-1]); got != 1 {
		t.Fatalf("expected isolated memory (1 message), got %d", got)
	}
}
