// Package mood maps sticker/emoji signals to coarse moods, mirroring the mood
// tables used by IotaXMedia's sticker-reply engine.
package mood

import "strings"

// emojiMood maps a single emoji to a mood bucket.
var emojiMood = map[rune]string{
	'😂': "laugh", '🤣': "laugh", '😹': "laugh", '💀': "laugh",
	'😍': "love", '🥰': "love", '💕': "love", '💞': "love",
	'❤': "love", '🫶': "love", '💝': "love", '💘': "love",
	'😭': "sad", '😢': "sad", '💔': "sad", '😔': "sad", '😞': "sad",
	'😡': "angry", '🤬': "angry", '😤': "angry", '💢': "angry",
	'🥳': "happy", '😊': "happy", '😁': "happy", '🎉': "happy",
	'😄': "happy", '🤩': "happy", '🎊': "happy",
	'😮': "surprise", '😱': "surprise", '😲': "surprise", '👀': "surprise",
	'💃': "dance", '🕺': "dance", '🎵': "dance", '🎶': "dance",
	'😎': "cool", '🤙': "cool", '✌': "cool",
	'🥺': "cute", '🌸': "cute", '✨': "cute", '🌷': "cute",
	'🍑': "cute", '🌼': "cute",
}

// related maps a mood to adjacent moods to try when the exact pack is empty.
var related = map[string][]string{
	"happy":    {"laugh", "dance", "cute", "cool"},
	"laugh":    {"happy", "cute", "dance"},
	"love":     {"cute", "sad", "happy"},
	"cute":     {"love", "happy", "laugh"},
	"sad":      {"love", "cute"},
	"angry":    {"cool", "surprise"},
	"surprise": {"cool", "angry"},
	"dance":    {"happy", "cool", "laugh"},
	"cool":     {"happy", "dance", "surprise"},
	"default":  {},
}

// Default is the fallback mood when nothing matches.
const Default = "default"

// FromEmoji returns the mood for the first recognised emoji in s, or "default".
func FromEmoji(s string) string {
	for _, r := range s {
		if m, ok := emojiMood[r]; ok {
			return m
		}
	}
	return Default
}

// FromText returns a mood guessed from emoji or English/Hinglish keywords.
func FromText(s string) string {
	if m := FromEmoji(s); m != Default {
		return m
	}
	l := strings.ToLower(s)
	switch {
	case containsAny(l, "dance", "vibe", "music", "song", "gaana"):
		return "dance"
	case containsAny(l, "love", "kiss", "heart", "pyaar", "pyar"):
		return "love"
	case containsAny(l, "funny", "lol", "haha", "laugh", "hasi"):
		return "laugh"
	case containsAny(l, "angry", "mad", "rage", "gussa"):
		return "angry"
	case containsAny(l, "cute", "kawaii", "aww", "soft"):
		return "cute"
	}
	return Default
}

// Related returns the moods to try after the exact match, most relevant first.
func Related(m string) []string {
	if r, ok := related[strings.ToLower(strings.TrimSpace(m))]; ok {
		return r
	}
	return nil
}

// Candidates returns the lookup order for a mood: the mood itself, then related.
func Candidates(m string) []string {
	m = strings.ToLower(strings.TrimSpace(m))
	if m == "" {
		m = Default
	}
	out := []string{m}
	for _, r := range Related(m) {
		if r != m {
			out = append(out, r)
		}
	}
	return out
}

// Sanitize normalises free-form text into a mood slug.
func Sanitize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range s {
		switch {
		case r == ' ' || r == '_' || r == '-' || r == '/' || r == '.':
			if b.Len() > 0 && !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastUnderscore = false
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return Default
	}
	return out
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
