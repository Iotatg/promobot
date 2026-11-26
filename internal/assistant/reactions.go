package assistant

import (
	"context"
	"log/slog"
	"math/rand"
	"regexp"
	"strings"
	"time"

	"github.com/gotd/td/tg"
)

// Reaction modes control how a reaction emoji is chosen.
const (
	ReactModeOffline = "offline" // keyword/emoji engine only
	ReactModeAI      = "ai"      // model decides only
	ReactModeBoth    = "both"    // offline first, model as fallback (default)
)

// This file ports IotaXMedia's offline auto-reaction engine
// (IotaXMedia/utils/auto_react.py) to Go. It is pure keyword/emoji/sticker
// detection with no LLM: it decides when the assistant should drop a single
// Telegram emoji reaction on a message. The wiring lives in maybeReact, called
// from processMessage for every non-outgoing message.

// reactAllowed is the set of emojis Telegram accepts as free reactions.
var reactAllowed = map[string]bool{
	"👍": true, "👎": true, "❤": true, "🔥": true, "🥰": true, "👏": true,
	"😁": true, "🤔": true, "🤯": true, "😱": true, "🤬": true, "😢": true,
	"🎉": true, "🤩": true, "🙏": true, "👌": true, "🕊": true, "🤡": true,
	"🥱": true, "🥴": true, "😍": true, "🐳": true, "🌚": true, "🌭": true,
	"💯": true, "🤣": true, "⚡": true, "🍌": true, "🏆": true, "💔": true,
	"🤨": true, "😐": true, "🍓": true, "🍾": true, "💋": true, "🖕": true,
	"😈": true, "😴": true, "😭": true, "🤓": true, "👻": true, "👀": true,
	"🎃": true, "🙈": true, "😇": true, "😨": true, "🤝": true, "✍": true,
	"🤗": true, "🫡": true, "🎅": true, "🎄": true, "☃": true, "💅": true,
	"🤪": true, "🗿": true, "🆒": true, "💘": true, "🙉": true, "🦄": true,
	"😘": true, "💊": true, "🙊": true, "😎": true, "👾": true, "🤷": true,
	"😡": true,
}

// reactMoodPool maps an AI-decided mood to the type of emojis the assistant may
// react with. Every emoji here is a Telegram free reaction.
var reactMoodPool = map[string][]string{
	"happy":  {"😁", "🤩", "🎉", "😍"},
	"love":   {"❤", "🥰", "😍", "😘"},
	"funny":  {"😂", "🤣", "😁"},
	"angry":  {"😡", "🤬"},
	"sad":    {"😢", "💔"},
	"shock":  {"😱", "🤯"},
	"hype":   {"🔥", "🤩", "💯"},
	"cool":   {"😎", "🔥"},
	"sleepy": {"😴", "🥱"},
	"like":   {"👍", "👏", "🙏"},
}

// pickMoodReactions returns 1-3 allowed emojis from the mood's pool, so the
// assistant reacts with a mood-appropriate set instead of always one emoji.
// An unknown/none mood yields nil (no reaction).
func pickMoodReactions(mood string) []string {
	pool := reactMoodPool[strings.ToLower(strings.TrimSpace(mood))]
	if len(pool) == 0 {
		return nil
	}
	allowed := make([]string, 0, len(pool))
	for _, e := range pool {
		if reactAllowed[e] {
			allowed = append(allowed, e)
		}
	}
	if len(allowed) == 0 {
		return nil
	}
	rand.Shuffle(len(allowed), func(i, j int) { allowed[i], allowed[j] = allowed[j], allowed[i] })
	want := 2
	if rand.Float64() < 0.4 {
		want = 3
	}
	if want > len(allowed) {
		want = len(allowed)
	}
	return allowed[:want]
}

// reactEmojiMap maps an incoming emoji to a small set of reactions to echo.
var reactEmojiMap = map[string][]string{
	"😂": {"🤣", "😁"}, "🤣": {"😂", "😁"}, "😁": {"😂", "🤣"},
	"😍": {"😍", "❤"}, "🥰": {"🥰", "❤"}, "😘": {"😘", "🥰"},
	"❤": {"❤", "🥰"}, "💘": {"💘", "❤"}, "💔": {"💔", "😢"},
	"😢": {"😢", "💔"}, "😭": {"😢", "💔"},
	"😡": {"😡", "🤬"}, "🤬": {"🤬", "😡"},
	"🎉": {"🎉", "🤩"}, "🥳": {"🎉", "🤩"}, "🔥": {"🔥", "🤩"},
	"😱": {"😱", "🤯"}, "🤯": {"🤯", "😱"}, "😎": {"😎", "🔥"},
	"👏": {"👏", "👍"}, "👍": {"👍", "👏"}, "🙏": {"🙏", "👏"},
	"💯": {"💯", "🔥"}, "🤩": {"🤩", "🔥"}, "🤗": {"🤗", "🥰"},
	"😈": {"😈", "🤬"}, "😴": {"😴", "🥱"}, "🥱": {"🥱", "😴"},
	"🤡": {"🤡", "😂"}, "🤨": {"🤨", "😐"}, "😐": {"😐", "🤨"},
	"🤔": {"🤔", "😐"}, "🤭": {"😁", "😂"}, "🥺": {"😢", "🥰"},
	"🤪": {"🤪", "😂"}, "👻": {"👻", "😂"}, "🙈": {"🙈", "🤣"},
	"🤓": {"🤓", "👍"}, "😇": {"😇", "🥰"}, "😨": {"😨", "😱"},
	"🤝": {"🤝", "🙏"}, "✍": {"✍", "👍"}, "🫡": {"🫡", "👍"},
	"⚡": {"⚡", "🔥"}, "🏆": {"🏆", "🎉"}, "🍌": {"🍌", "😂"},
	"🌚": {"🌚", "😈"}, "🦄": {"🦄", "😍"}, "🐳": {"🐳", "😂"},
}

// reactRule is a keyword pattern plus the mood and emoji pool it maps to.
type reactRule struct {
	re     *regexp.Regexp
	mood   string
	emojis []string
}

// reactRules mirrors IotaXMedia's _RULES, tuned for a real-girl persona
// (Hinglish + English), not the music-bot original.
var reactRules = []reactRule{
	{regexp.MustCompile(`(?i)\b(jeet g(?:a)?y(?:a)?|jeet liya|jeet li|won|winner|we won|i won|champion|victory|number one|new highscore|top scorer)\b`), "win", []string{"🏆", "🎉", "🔥"}},
	{regexp.MustCompile(`(?i)\b(happy birthday|hbd|congrats|congratulations|badhai|mubarak|birthday|anniversary)\b`), "party", []string{"🎉", "🤩"}},
	{regexp.MustCompile(`(?i)\b(lol|lmao|rofl|haha+|hehe+|hihi+|funny|joke|mazak|hilarious|roflmao)\b`), "funny", []string{"🤣", "🔥"}},
	{regexp.MustCompile(`(?i)\b(sad|dukhi|dukh|ro raha|ro rahi|roya|rone|rula|upset|depressed|dead|died|mar gaya|mar gayi|miss you|yaad aati|lonely|breakup|dhokha|tuta|tute)\b`), "sad", []string{"😢", "💔"}},
	{regexp.MustCompile(`(?i)\b(i love you|love you|love u|i love|pyaar|ishq|mohabbat|shaadi|marry me|wife|husband|crush|babe|cutie|meri jaan|dil)\b`), "love", []string{"❤", "🥰"}},
	{regexp.MustCompile(`(?i)\b(you'?re (so )?(cute|smart|amazing|best|great|beautiful|lovely)|tum (kitni|bahut|itni) (cute|pyari|acha|acchi|khoobsurat)|good (job|work)|well done|shabash|great work)\b`), "compliment", []string{"🔥", "😍"}},
	{regexp.MustCompile(`(?i)\b(wow+|omg|oh my god|ohh|wtf|shocking|surprise|what+|kya baat|kaisa hua|no way|really!)\b`), "shock", []string{"😱", "🤯"}},
	{regexp.MustCompile(`(?i)\b(fire|lit|awesome|epic|goated|insane|op|beast|legend|fantastic|superb|killer|monster|crush)\b`), "hype", []string{"🔥", "🤩"}},
	{regexp.MustCompile(`(?i)\b(angry|gussa|gusse|irritat|annoy|pissed|hate|naraz|bht gussa)\b`), "anger", []string{"😡", "🤬"}},
	{regexp.MustCompile(`(?i)\b(thank you|thanks|thx|thank u|tysm|dhanyawad|shukriya)\b`), "thanks", []string{"🙏", "👏"}},
	{regexp.MustCompile(`(?i)\b(good morning|good evening|suprabhat|namaste|welcome back|gud mrng)\b`), "greet", []string{"🤩", "😁"}},
	{regexp.MustCompile(`(?i)\b(goodbye|alvida|cya|see you|tata bye|bye bye)\b`), "bye", []string{"🤝", "👍"}},
	{regexp.MustCompile(`(?i)\b(party|celebrat|enjoy|masti|chill|rave|concert|hangout)\b`), "party", []string{"🎉", "🔥"}},
	{regexp.MustCompile(`(?i)\b(music|song|gaana|gana|track|melody|beat|playlist|album|lyrics|remix|drop the beat|listen|banger|track dropped)\b`), "music", []string{"🔥", "🎉"}},
	{regexp.MustCompile(`(?i)\b(cute|adorable|sweet|pyara|pyari|soo cute|kawaii|aww|soft|innocent)\b`), "cute", []string{"🥰", "😍"}},
	{regexp.MustCompile(`(?i)\b(dance|nacho|nach|groove|step it up|thumka|bhangra|drop the)\b`), "dance", []string{"🔥", "😎"}},
	{regexp.MustCompile(`(?i)\b(burn|roast|savage|clapback|rekt|destroyed|spicy reply)\b`), "roast", []string{"🔥", "🤣"}},
	{regexp.MustCompile(`(?i)\b(money|paise|paisa|coins|gems|rich|profit|jackpot|lottery|cash)\b`), "money", []string{"💯", "🔥"}},
	{regexp.MustCompile(`(?i)\b(sleep|so jao|so gaya|nind|neend|tired|thak gya|thak gyi|exhausted)\b`), "sleep", []string{"😴", "🥱"}},
	{regexp.MustCompile(`(?i)\b(cool|nice|super|smooth|sigma|chad|goat|great move)\b`), "cool", []string{"😎", "🔥"}},
	{regexp.MustCompile(`(?i)\b(all the best|good luck|best of luck|shubh kamnaye)\b`), "luck", []string{"🙏", "👍"}},
}

// reactPersonality biases emoji choice toward the persona's taste for some
// moods (mirrors PERSONALITY["pool"]).
var reactPersonality = map[string][]string{
	"hype": {"🔥", "🤩"}, "music": {"🔥", "🎉"}, "party": {"🎉", "🔥"},
	"win": {"🏆", "🔥"}, "dance": {"🔥", "😎"}, "love": {"❤", "🥰"},
	"cool": {"😎", "🔥"}, "compliment": {"🔥", "😍"}, "funny": {"🤣", "🔥"},
	"cute": {"🥰", "😍"}, "greet": {"🤩", "😁"},
}

// reactNeutral are low-content messages that never earn a reaction.
var reactNeutral = map[string]bool{
	"ok": true, "okay": true, "okk": true, "okayy": true, "k": true, "kk": true,
	"yes": true, "no": true, "yep": true, "yeah": true, "nah": true, "nope": true,
	"hmm": true, "um": true, "umm": true, "mm": true, "m": true, "idk": true,
	"same": true, "wtv": true, "oh": true, "ohh": true, "acha": true, "accha": true,
	"haan": true, "ha": true, "na": true, "nai": true, "ji": true, "sir": true,
	"kya": true, "kyu": true, "why": true, "hm": true, "done": true, "thik": true,
	"theek": true,
}

const (
	reactProbability = 0.30
	reactCooldown    = 10 * time.Second
	reactBiasChance  = 0.35
)

// reactRequestRe matches an explicit ask to react ("react to this", "reaction
// do", "ek reaction de", "react with 🔥").
var reactRequestRe = regexp.MustCompile(`(?i)\b(react|reaction|react karo|react do|react de|reaction do|reaction de)\b`)

// wantsReact reports whether the message explicitly asks the assistant to react.
func wantsReact(text string) bool {
	return reactRequestRe.MatchString(text)
}

// maybeReact fires a single emoji reaction on m, best-effort and never fatal.
// It mirrors IotaXMedia's maybe_react: skips outgoing/bot/command messages,
// respects a per-chat cooldown and an overall probability, and reacts to a
// sticker's emoji or to text that clearly calls for it. An explicit request
// ("react to this") bypasses the probability and cooldown gates.
func (c *Client) maybeReact(ctx context.Context, m *tg.Message) {
	if m == nil || m.Out || c.api == nil || c.pm == nil {
		return
	}
	if c.selfID != 0 {
		if u, ok := m.FromID.(*tg.PeerUser); ok && u.UserID == c.selfID {
			return
		}
	}
	text := strings.TrimSpace(m.Message)
	forced := wantsReact(text)
	if !forced && (strings.HasPrefix(text, "/") || strings.HasPrefix(text, ".")) {
		return
	}

	chatID := chatIDFromPeer(m.PeerID)
	now := time.Now()
	mode := c.reactModeValue()
	if !forced {
		c.reactMu.Lock()
		if last, ok := c.lastReact[chatID]; ok && now.Sub(last) < reactCooldown {
			c.reactMu.Unlock()
			return
		}
		c.reactMu.Unlock()
	}

	var chosen []string
	if forced {
		// Honor an explicit emoji in the request ("react with 🔥").
		if e := emojiFromText(text); e != "" {
			chosen = []string{e}
		}
	}
	// Offline engine (skipped when the mode is ai-only). It keeps its own
	// probability gate so keyword reactions stay occasional.
	if len(chosen) == 0 && mode != ReactModeAI {
		if forced || rand.Float64() <= reactProbability {
			if isSticker(m) {
				if doc := messageDoc(m); doc != nil {
					if e := chooseStickerReaction(emojiFromDoc(doc)); e != "" {
						chosen = []string{e}
					}
				}
			}
			if len(chosen) == 0 && text != "" {
				if e := chooseTextReaction(text); e != "" {
					chosen = []string{e}
				}
			}
		}
	}
	// Model decision (ai or both modes): the assistant classifies the mood and
	// reacts with that type of emoji (a small set), or nothing for "none".
	if len(chosen) == 0 && (mode == ReactModeAI || mode == ReactModeBoth) && c.ai != nil {
		if mood, err := c.ai.DecideMood(ctx, reactionPromptText(m, text)); err == nil {
			chosen = pickMoodReactions(mood)
			slog.Debug("ai reaction mood", "mood", mood, "emojis", chosen)
		} else {
			slog.Debug("ai reaction decide failed", "err", err)
		}
	}
	if len(chosen) == 0 && forced {
		chosen = []string{"❤"}
	}
	if len(chosen) == 0 {
		return
	}
	cleaned := make([]string, 0, len(chosen))
	for _, e := range chosen {
		if reactAllowed[e] {
			cleaned = append(cleaned, e)
		}
	}
	if len(cleaned) == 0 {
		cleaned = []string{"👍"}
	}
	chosen = cleaned

	peer, err := c.inputPeer(chatID)
	if err != nil {
		slog.Warn("assistant reaction: resolve peer failed", "chat", chatID, "err", err)
		return
	}
	c.reactMu.Lock()
	c.lastReact[chatID] = now
	c.reactMu.Unlock()

	if _, err := c.sendReaction(ctx, peer, m.ID, chosen); err != nil {
		// Some accounts (non-premium) accept only a single free reaction; retry
		// with just the first emoji before giving up.
		if len(chosen) > 1 {
			if _, err2 := c.sendReaction(ctx, peer, m.ID, chosen[:1]); err2 != nil {
				slog.Warn("assistant reaction failed", "chat", chatID, "msg", m.ID, "emoji", chosen, "err", err2)
				return
			}
			slog.Info("assistant reacted", "chat", chatID, "msg", m.ID, "emoji", chosen[:1], "forced", forced)
			return
		}
		slog.Warn("assistant reaction failed", "chat", chatID, "msg", m.ID, "emoji", chosen, "err", err)
		return
	}
	slog.Info("assistant reacted", "chat", chatID, "msg", m.ID, "emoji", chosen, "forced", forced)
}

// sendReaction sends one or more emoji reactions on a message.
func (c *Client) sendReaction(ctx context.Context, peer tg.InputPeerClass, msgID int, emojis []string) (any, error) {
	req := &tg.MessagesSendReactionRequest{Peer: peer, MsgID: msgID}
	classes := make([]tg.ReactionClass, 0, len(emojis))
	for _, e := range emojis {
		classes = append(classes, &tg.ReactionEmoji{Emoticon: e})
	}
	req.SetReaction(classes)
	return c.api.MessagesSendReaction(ctx, req)
}

// emojiFromText returns the first allowed emoji found in text, or "".
func emojiFromText(text string) string {
	for _, r := range text {
		if reactAllowed[string(r)] {
			return string(r)
		}
	}
	return ""
}

// pickReact returns a random allowed emoji from candidates, defaulting to 👍.
func pickReact(candidates []string) string {
	allowed := make([]string, 0, len(candidates))
	for _, e := range candidates {
		if reactAllowed[e] {
			allowed = append(allowed, e)
		}
	}
	if len(allowed) == 0 {
		return "👍"
	}
	return allowed[rand.Intn(len(allowed))]
}

// chooseTextReaction returns an emoji for text, or "" when nothing fits.
func chooseTextReaction(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	low := strings.ToLower(text)
	for _, r := range text {
		if opts, ok := reactEmojiMap[string(r)]; ok {
			return pickReact(opts)
		}
	}
	if isNeutralReact(low) {
		return ""
	}
	for _, rule := range reactRules {
		if rule.re.MatchString(text) {
			pool := rule.emojis
			if pref, ok := reactPersonality[rule.mood]; ok && rand.Float64() < reactBiasChance {
				pool = pref
			}
			return pickReact(pool)
		}
	}
	return ""
}

// chooseStickerReaction returns an emoji matching a sticker's own emoji.
func chooseStickerReaction(stickerEmoji string) string {
	se := strings.TrimSpace(stickerEmoji)
	if se == "" {
		return ""
	}
	for _, r := range se {
		if opts, ok := reactEmojiMap[string(r)]; ok {
			return pickReact(opts)
		}
	}
	for _, r := range se {
		if reactAllowed[string(r)] {
			return string(r)
		}
	}
	return ""
}

// reactModeValue returns the current reaction mode, defaulting to both.
func (c *Client) reactModeValue() string {
	c.reactMu.Lock()
	defer c.reactMu.Unlock()
	switch c.reactMode {
	case ReactModeOffline, ReactModeAI, ReactModeBoth:
		return c.reactMode
	default:
		return ReactModeBoth
	}
}

// SetReactMode sets the reaction mode (offline, ai or both). Unknown values are
// ignored.
func (c *Client) SetReactMode(mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case ReactModeOffline, ReactModeAI, ReactModeBoth:
	default:
		return
	}
	c.reactMu.Lock()
	c.reactMode = mode
	c.reactMu.Unlock()
}

// reactionPromptText is the text handed to the model when it decides a reaction.
func reactionPromptText(m *tg.Message, text string) string {
	if text != "" {
		return text
	}
	if isSticker(m) {
		if doc := messageDoc(m); doc != nil {
			if e := emojiFromDoc(doc); e != "" {
				return "sticker: " + e
			}
		}
	}
	return ""
}

// isNeutralReact reports whether text is too low-content to react to.
func isNeutralReact(low string) bool {
	t := strings.TrimRight(strings.TrimSpace(low), ".!? ")
	if reactNeutral[t] {
		return true
	}
	if len([]rune(t)) <= 2 && !strings.ContainsAny(t, "0123456789") {
		return true
	}
	return false
}
