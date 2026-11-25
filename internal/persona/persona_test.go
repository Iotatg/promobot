package persona

import (
	"strings"
	"testing"
)

func TestWithNameOverridesIdentity(t *testing.T) {
	sys := Default().WithName("Riya").System()
	if !strings.Contains(sys, "You are Riya") {
		t.Errorf("custom name not applied: %q", firstLine(sys))
	}
	// Empty name keeps the default identity.
	if got := Default().WithName("  ").System(); !strings.Contains(got, "You are Radhika") {
		t.Error("empty custom name should keep the default identity")
	}
}

func TestAppendDetails(t *testing.T) {
	sys := Default().System()
	if AppendDetails(sys, "   ") != sys {
		t.Error("empty details must not change the prompt")
	}
	out := AppendDetails(sys, "reply only in english")
	if !strings.Contains(out, "Owner custom persona") || !strings.Contains(out, "reply only in english") {
		t.Errorf("custom details not appended: %q", out)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
