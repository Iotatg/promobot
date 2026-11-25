package mood

import (
	"reflect"
	"testing"
)

func TestFromEmoji(t *testing.T) {
	cases := map[string]string{
		"😂":   "laugh",
		"❤️":  "love",
		"😍":   "love",
		"😭":   "sad",
		"😡":   "angry",
		"🥳":   "happy",
		"":    Default,
		"abc": Default,
		"🔥🔥":  Default,
	}
	for in, want := range cases {
		if got := FromEmoji(in); got != want {
			t.Errorf("FromEmoji(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFromText(t *testing.T) {
	cases := map[string]string{
		"let us dance tonight": "dance",
		"I love you so much":   "love",
		"so funny lol haha":    "laugh",
		"this is gussa":        "angry",
		"so cute aww":          "cute",
		"just a normal line":   Default,
	}
	for in, want := range cases {
		if got := FromText(in); got != want {
			t.Errorf("FromText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFromTextPrefersEmoji(t *testing.T) {
	if got := FromText("lol 😭"); got != "sad" {
		t.Errorf("FromText emoji precedence = %q, want sad", got)
	}
}

func TestCandidates(t *testing.T) {
	got := Candidates("happy")
	if len(got) == 0 || got[0] != "happy" {
		t.Fatalf("Candidates(happy) = %v, want first element happy", got)
	}
	if !contains(got, "laugh") {
		t.Errorf("Candidates(happy) = %v, want it to include a related mood", got)
	}

	if def := Candidates(""); !reflect.DeepEqual(def, []string{Default}) {
		t.Errorf("Candidates(\"\") = %v, want [default]", def)
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"  Hello World! ": "hello_world",
		"Sad/Songs":       "sad_songs",
		"a..b--c":         "a_b_c",
		"???":             Default,
		"UPPER_case":      "upper_case",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
