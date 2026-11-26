package ai

import "testing"

func TestBlindReply(t *testing.T) {
	blind := []string{
		"arre suna nahi properly yaar, phir se bol na, kya tha isme",
		"sunai nahi de raha dobara bhejo",
		"samajh nahi aaya kya bola",
		"phir se bolo",
		"couldn't make out the audio",
		"i didn't catch that",
		"please repeat",
		"no audio received",
	}
	for _, s := range blind {
		if !blindReply.MatchString(s) {
			t.Errorf("expected blind: %q", s)
		}
	}
	ok := []string{
		"ye gaana accha hai, lyrics meaningful lage",
		"bhai voice note suna, mazedaar tha",
		"the audio says: kal milte hain",
		"ek tone sunai di jo lagaataar chal raha tha",
	}
	for _, s := range ok {
		if blindReply.MatchString(s) {
			t.Errorf("unexpected blind match: %q", s)
		}
	}
}
