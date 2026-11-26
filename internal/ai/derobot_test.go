package ai

import (
	"strings"
	"testing"
)

func TestDeRobotize(t *testing.T) {
	cases := []struct {
		in   string
		bad  []string // must NOT appear
		want string   // exact expected (when non-empty)
	}{
		{
			in:  "ohh mumbai se, nice yaar 😏 hehe, tu bata kya scene hai tera, jaipur aaya kabhi? Mumbai toh kaafi door hai na, kya kaam hai tu wahan pe ab ye kya majak hai tu bata kya scene hai",
			bad: []string{"kya scene hai", "kya kaam hai", "ye kya majak hai"},
		},
		{
			in:  "mansarovar side se yaar, tu bata tu kahan ka hai 😏 hehe, kya scene hai tera, jaipur mein hi hai tu ya kahin aur",
			bad: []string{"kya scene hai"},
		},
		{
			in:  "bata na kya chahiye 😏 hehe",
			bad: []string{"bata na kya chahiye"},
		},
		{
			in:   "kya scene hai",
			want: "hmm",
		},
		{
			in:   "haan bolo jaan",
			want: "haan bolo jaan",
		},
	}
	for _, tc := range cases {
		got := deRobotize(tc.in)
		for _, b := range tc.bad {
			if strings.Contains(strings.ToLower(got), b) {
				t.Errorf("deRobotize(%q) still contains %q: %q", tc.in, b, got)
			}
		}
		if tc.want != "" && got != tc.want {
			t.Errorf("deRobotize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCapHelpers(t *testing.T) {
	if got := capWord("tu bata tu bata tu bata", "tu bata", 1); strings.Count(strings.ToLower(got), "tu bata") != 1 {
		t.Errorf("capWord kept too many: %q", got)
	}
	if got := capEmoji("😏😏😏 hi", "😏", 1); strings.Count(got, "😏") != 1 {
		t.Errorf("capEmoji kept too many: %q", got)
	}
	if got := capWord("hehe hehe hehe", "hehe", 1); strings.Count(strings.ToLower(got), "hehe") != 1 {
		t.Errorf("capWord hehe failed: %q", got)
	}
}
