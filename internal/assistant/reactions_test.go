package assistant

import "testing"

func TestChooseTextReaction(t *testing.T) {
	cases := []struct {
		text string
		want bool // true = should react
	}{
		{"bhai ye toh fire tha", true},
		{"haha lol", true},
		{"i love you", true},
		{"happy birthday yaar", true},
		{"ok", false},
		{"hmm", false},
		{"k", false},
		{"", false},
		{"/start", false},
	}
	for _, tc := range cases {
		got := chooseTextReaction(tc.text)
		if (got != "") != tc.want {
			t.Errorf("chooseTextReaction(%q) = %q, want react=%v", tc.text, got, tc.want)
		}
	}
}

func TestChooseStickerReaction(t *testing.T) {
	if got := chooseStickerReaction("😂"); got == "" {
		t.Errorf("expected a reaction for 😂")
	}
	if got := chooseStickerReaction(""); got != "" {
		t.Errorf("expected no reaction for empty emoji, got %q", got)
	}
}

func TestPickReactAllowed(t *testing.T) {
	for _, cand := range [][]string{{"👍"}, {"🔥", "🤩"}, {"not-an-emoji"}} {
		got := pickReact(cand)
		if !reactAllowed[got] && got != "👍" {
			t.Errorf("pickReact(%v) = %q, not allowed", cand, got)
		}
	}
}

func TestWantsReact(t *testing.T) {
	yes := []string{"react to this", "ek reaction do", "REACT", "reaction de na", "react with 🔥"}
	no := []string{"i am tired", "kya scene hai", "hello"}
	for _, s := range yes {
		if !wantsReact(s) {
			t.Errorf("wantsReact(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if wantsReact(s) {
			t.Errorf("wantsReact(%q) = true, want false", s)
		}
	}
}

func TestEmojiFromText(t *testing.T) {
	if got := emojiFromText("react with 🔥 please"); got != "🔥" {
		t.Errorf("emojiFromText = %q, want 🔥", got)
	}
	if got := emojiFromText("no emoji here"); got != "" {
		t.Errorf("emojiFromText = %q, want empty", got)
	}
}

func TestPickMoodReactions(t *testing.T) {
	for _, mood := range []string{"happy", "love", "funny", "angry", "sad", "shock", "hype", "cool", "sleepy", "like"} {
		got := pickMoodReactions(mood)
		if len(got) < 1 || len(got) > 3 {
			t.Errorf("pickMoodReactions(%q) len = %d, want 1..3", mood, len(got))
		}
		for _, e := range got {
			if !reactAllowed[e] {
				t.Errorf("pickMoodReactions(%q) returned disallowed %q", mood, e)
			}
		}
	}
	if got := pickMoodReactions("none"); got != nil {
		t.Errorf("pickMoodReactions(none) = %v, want nil", got)
	}
	if got := pickMoodReactions("bogus"); got != nil {
		t.Errorf("pickMoodReactions(bogus) = %v, want nil", got)
	}
}
