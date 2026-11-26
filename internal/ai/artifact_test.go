package ai

import "testing"

func TestSanitizeArtifacts(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain unchanged", "hello world", "hello world"},
		{"tag only line dropped", "hi\n</rfl:parameter>\nthere", "hi\nthere"},
		{"leading tag line", "</rfl:parameter>\nnamaste", "namaste"},
		{"inline tag stripped", "hello </rfl:parameter> world", "hello world"},
		{"opening tag", "<rfl:parameter>", ""},
		{"keeps urls", "see https://example.com/a:b", "see https://example.com/a:b"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		if got := sanitizeArtifacts(tc.in); got != tc.want {
			t.Errorf("%s: sanitizeArtifacts(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}
