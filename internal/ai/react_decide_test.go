package ai

import (
	"context"
	"testing"
)

func TestDecideMood(t *testing.T) {
	srv, _ := stubServer(t, "happy")
	c := New("k", srv.URL, "m", "")
	got, err := c.DecideMood(context.Background(), "bhai ye toh mast tha")
	if err != nil {
		t.Fatalf("DecideMood: %v", err)
	}
	if got != "happy" {
		t.Errorf("DecideMood = %q, want happy", got)
	}
}

func TestDecideMoodNone(t *testing.T) {
	srv, _ := stubServer(t, "NONE")
	c := New("k", srv.URL, "m", "")
	got, err := c.DecideMood(context.Background(), "ok")
	if err != nil {
		t.Fatalf("DecideMood: %v", err)
	}
	if got != "none" {
		t.Errorf("DecideMood = %q, want none", got)
	}
}

func TestDecideMoodNormalizes(t *testing.T) {
	srv, _ := stubServer(t, "The mood is clearly ANGRY.")
	c := New("k", srv.URL, "m", "")
	got, err := c.DecideMood(context.Background(), "uff gussa aa raha hai")
	if err != nil {
		t.Fatalf("DecideMood: %v", err)
	}
	if got != "angry" {
		t.Errorf("DecideMood = %q, want angry", got)
	}
}

func TestClearAllMemory(t *testing.T) {
	srv, _ := stubServer(t, "ok")
	fm := newFakeMem()
	c := New("k", srv.URL, "m", "")
	c.SetMemory(fm, "acct1")
	ctx := context.Background()

	if _, err := c.AskSession(ctx, "chat:1", "q1"); err != nil {
		t.Fatalf("ask1: %v", err)
	}
	if _, err := c.AskSession(ctx, "chat:2", "q2"); err != nil {
		t.Fatalf("ask2: %v", err)
	}
	if len(fm.m) != 2 {
		t.Fatalf("expected 2 stored docs, got %d", len(fm.m))
	}

	n, err := c.ClearAllMemory(ctx)
	if err != nil {
		t.Fatalf("ClearAllMemory: %v", err)
	}
	if n != 2 {
		t.Errorf("ClearAllMemory removed %d, want 2", n)
	}
	if len(fm.m) != 0 {
		t.Errorf("memory not cleared: %d docs remain", len(fm.m))
	}
	// In-memory cache must be empty too.
	if got := c.RecentContext(ctx, "chat:1", 8); got != "" {
		t.Errorf("expected empty context after clear, got %q", got)
	}
}

func TestClearAllMemoryNoNamespace(t *testing.T) {
	srv, _ := stubServer(t, "ok")
	fm := newFakeMem()
	c := New("k", srv.URL, "m", "")
	c.SetMemory(fm, "") // no namespace
	ctx := context.Background()
	if _, err := c.AskSession(ctx, "chat:1", "q"); err != nil {
		t.Fatalf("ask: %v", err)
	}
	n, err := c.ClearAllMemory(ctx)
	if err != nil {
		t.Fatalf("ClearAllMemory: %v", err)
	}
	if n != 0 {
		t.Errorf("namespaceless clear removed %d, want 0", n)
	}
	// Without a namespace the store is left untouched (only namespaced
	// assistant accounts are cleared in practice), so history reloads.
	if len(fm.m) != 1 {
		t.Errorf("expected store untouched, got %d docs", len(fm.m))
	}
}
