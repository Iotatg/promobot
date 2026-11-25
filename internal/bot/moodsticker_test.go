package bot

import "testing"

// TestCyclePickNoRepeat ensures stickers cycle without repeating the previous
// pick until the pool is exhausted.
func TestCyclePickNoRepeat(t *testing.T) {
	b := &Bot{}
	ids := []string{"a", "b", "c"}
	seen := make(map[string]int)
	prev := ""
	for i := 0; i < 6; i++ {
		p := b.cyclePick("1|mood", ids)
		if p == prev {
			t.Fatalf("pick %d repeated previous sticker %q", i, p)
		}
		seen[p]++
		prev = p
	}
	if len(seen) != 3 {
		t.Fatalf("expected all 3 stickers used, got %v", seen)
	}
}

// TestCyclePickSingleSticker verifies a single-sticker pack never panics.
func TestCyclePickSingleSticker(t *testing.T) {
	b := &Bot{}
	for i := 0; i < 3; i++ {
		if p := b.cyclePick("1|one", []string{"only"}); p != "only" {
			t.Fatalf("expected only, got %q", p)
		}
	}
}
