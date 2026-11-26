package assistant

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"

	"promobot/internal/mood"
)

// docRefPrefix marks a stored sticker reference as a portable gotd document
// reference ("mt:<id>:<access_hash>:<dc>:<file_reference>"), which the assistant
// can send directly. References without this prefix are bot-side TDLib file ids.
const docRefPrefix = "mt:"

// encDocRef encodes a document into a storable reference string.
func encDocRef(doc *tg.Document) string {
	if doc == nil || doc.ID == 0 || doc.AccessHash == 0 {
		return ""
	}
	fr := base64.RawURLEncoding.EncodeToString(doc.FileReference)
	return fmt.Sprintf("%s%d:%d:%d:%s", docRefPrefix, doc.ID, doc.AccessHash, doc.DCID, fr)
}

// decDocRef decodes a stored reference back into a sendable document.
func decDocRef(s string) (*tg.Document, bool) {
	if !strings.HasPrefix(s, docRefPrefix) {
		return nil, false
	}
	parts := strings.SplitN(strings.TrimPrefix(s, docRefPrefix), ":", 4)
	if len(parts) != 4 {
		return nil, false
	}
	id, err1 := strconv.ParseInt(parts[0], 10, 64)
	ah, err2 := strconv.ParseInt(parts[1], 10, 64)
	dc, err3 := strconv.Atoi(parts[2])
	fr, err4 := base64.RawURLEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return nil, false
	}
	return &tg.Document{ID: id, AccessHash: ah, DCID: dc, FileReference: fr}, true
}

// isAssistantRef reports whether a stored reference is assistant-sendable.
func isAssistantRef(s string) bool { return strings.HasPrefix(s, docRefPrefix) }

// refDocID returns the document id encoded in an assistant reference, or 0.
func refDocID(s string) int64 {
	if doc, ok := decDocRef(s); ok {
		return doc.ID
	}
	return 0
}

// assistantRefs keeps only assistant-sendable references.
func assistantRefs(refs []string) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if isAssistantRef(r) {
			out = append(out, r)
		}
	}
	return out
}

// pickOther returns a random reference whose document id differs from
// incomingID (so the assistant never echoes the exact sticker it received),
// mirroring IotaXMedia's _pick_other.
func pickOther(refs []string, incomingID int64) string {
	if len(refs) == 0 {
		return ""
	}
	for _, i := range rand.Perm(len(refs)) {
		if refDocID(refs[i]) != incomingID {
			return refs[i]
		}
	}
	return refs[0]
}

// docSetInput extracts the sticker set the document belongs to, plus a cache
// key identifying it.
func docSetInput(doc *tg.Document) (tg.InputStickerSetClass, string, bool) {
	for _, a := range doc.Attributes {
		at, ok := a.(*tg.DocumentAttributeSticker)
		if !ok {
			continue
		}
		switch s := at.Stickerset.(type) {
		case *tg.InputStickerSetShortName:
			if s.ShortName != "" {
				return s, "short:" + s.ShortName, true
			}
		case *tg.InputStickerSetID:
			if s.ID != 0 {
				return s, fmt.Sprintf("id:%d", s.ID), true
			}
		}
	}
	return nil, "", false
}

// autoMoodFromSet picks the dominant mood from a pack's sticker emojis, falling
// back to a slug of the pack title, mirroring IotaXMedia's _auto_mood_from_pack.
func autoMoodFromSet(set *tg.MessagesStickerSet) string {
	tally := make(map[string]int)
	for _, d := range set.Documents {
		doc, ok := d.(*tg.Document)
		if !ok {
			continue
		}
		if m := mood.FromEmoji(emojiFromDoc(doc)); m != mood.Default {
			tally[m]++
		}
	}
	best, bestN := "", 0
	for m, n := range tally {
		if n > bestN {
			best, bestN = m, n
		}
	}
	if best != "" {
		return best
	}
	return mood.Sanitize(set.Set.Title)
}

// sendStickerRef sends a stored assistant reference to a chat, optionally
// quoting the message identified by replyTo.
func (c *Client) sendStickerRef(ctx context.Context, chatID int64, ref string, replyTo int) bool {
	doc, ok := decDocRef(ref)
	if !ok {
		return false
	}
	if err := c.sendSticker(chatID, doc, replyTo); err != nil {
		slog.Warn("assistant sticker ref send failed", "err", err)
		return false
	}
	return true
}

// replyStickerForMood sends a sticker from the first non-empty mood pack among
// the candidate moods (exact mood, then related), mirroring
// _reply_sticker_for_mood.
func (c *Client) replyStickerForMood(ctx context.Context, chatID int64, wantMood string, incomingID int64, replyTo int) bool {
	for _, cand := range mood.Candidates(wantMood) {
		refs, err := c.st.GetStickersForMood(ctx, cand)
		if err != nil {
			continue
		}
		refs = assistantRefs(refs)
		if len(refs) == 0 {
			continue
		}
		return c.sendStickerRef(ctx, chatID, pickOther(refs, incomingID), replyTo)
	}
	return false
}

// replyAnySticker sends a sticker from any mood pack, mirroring
// _reply_any_sticker.
func (c *Client) replyAnySticker(ctx context.Context, chatID int64, incomingID int64, replyTo int) bool {
	packs, err := c.st.ListStickerPacks(ctx)
	if err != nil {
		return false
	}
	var all []string
	for m := range packs {
		refs, err := c.st.GetStickersForMood(ctx, m)
		if err != nil {
			continue
		}
		all = append(all, assistantRefs(refs)...)
	}
	if len(all) == 0 {
		return false
	}
	return c.sendStickerRef(ctx, chatID, pickOther(all, incomingID), replyTo)
}

// autoReplyFromPack mirrors IotaXMedia's _auto_reply_from_pack: it learns the
// whole pack the received sticker belongs to (into a mood pack), then replies
// with a different sticker from that same pack.
func (c *Client) autoReplyFromPack(ctx context.Context, chatID int64, doc *tg.Document, replyTo int) bool {
	set, key, ok := docSetInput(doc)
	if !ok {
		return false
	}

	if cached := c.cachedPackMood(key); cached != "" {
		if refs, err := c.st.GetStickersForMood(ctx, cached); err == nil {
			if ar := assistantRefs(refs); len(ar) > 0 {
				if c.sendStickerRef(ctx, chatID, pickOther(ar, doc.ID), replyTo) {
					return true
				}
			}
		}
	}

	setObj, err := c.getSetInput(ctx, set)
	if err != nil || setObj == nil {
		return false
	}
	pmood := autoMoodFromSet(setObj)

	refs := make([]string, 0, len(setObj.Documents))
	for _, d := range setObj.Documents {
		if dd, ok := d.(*tg.Document); ok {
			if r := encDocRef(dd); r != "" {
				refs = append(refs, r)
			}
		}
	}
	c.rememberPackMood(key, pmood)

	for _, r := range refs {
		if err := c.st.AddStickerToPack(ctx, pmood, r, 0); err != nil {
			slog.Debug("auto-save sticker failed", "err", err)
		}
	}

	if len(refs) <= 1 {
		if r := encDocRef(doc); r != "" {
			_ = c.st.AddStickerToPack(ctx, pmood, r, 0)
		}
		return false
	}
	return c.sendStickerRef(ctx, chatID, pickOther(refs, doc.ID), replyTo)
}

func (c *Client) cachedPackMood(key string) string {
	c.packsMu.Lock()
	defer c.packsMu.Unlock()
	return c.autoSavedPacks[key]
}

func (c *Client) rememberPackMood(key, m string) {
	c.packsMu.Lock()
	if c.autoSavedPacks == nil {
		c.autoSavedPacks = make(map[string]string)
	}
	c.autoSavedPacks[key] = m
	c.packsMu.Unlock()
}

// getSetInput resolves a sticker set by short name or id.
func (c *Client) getSetInput(ctx context.Context, set tg.InputStickerSetClass) (*tg.MessagesStickerSet, error) {
	res, err := c.api.MessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{Stickerset: set})
	if err != nil {
		return nil, err
	}
	s, ok := res.(*tg.MessagesStickerSet)
	if !ok {
		return nil, fmt.Errorf("unexpected sticker set type %T", res)
	}
	return s, nil
}
