package assistant

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"

	"promobot/internal/ai"
	"promobot/internal/media"
	"promobot/internal/mood"
	"promobot/internal/store"
)

// User-facing media messages and limits, mirroring media_analyzer.py.
const (
	mediaTooBig       = "ye file badi hai yaar, chhoti bhej"
	mediaDownloadFail = "abhi download nahi ho paayi"
	mediaAnalyzeFail  = "abhi dekh nahi payi"
	mediaBusy         = "ek sec, pehle wali dekh rahi hoon"
)

const (
	maxInFlight = 2
	albumTTL    = 90 * time.Second
)

// messageMediaKind classifies a message's non-sticker media into an ai.Media*
// kind. It returns ok=false for messages without analysable media.
func messageMediaKind(m *tg.Message) (string, bool) {
	switch md := m.Media.(type) {
	case *tg.MessageMediaPhoto:
		return ai.MediaPhoto, true
	case *tg.MessageMediaDocument:
		doc, _ := md.Document.(*tg.Document)
		if doc == nil {
			return "", false
		}
		kind := docKind(doc)
		if kind == "" {
			return "", false
		}
		return kind, true
	}
	return "", false
}

// docKind maps a document's attributes/mime to a media kind. Stickers return "".
func docKind(doc *tg.Document) string {
	for _, a := range doc.Attributes {
		switch at := a.(type) {
		case *tg.DocumentAttributeSticker:
			return ""
		case *tg.DocumentAttributeAudio:
			_ = at
			return ai.MediaAudio
		case *tg.DocumentAttributeVideo:
			return ai.MediaVideo
		case *tg.DocumentAttributeAnimated:
			return ai.MediaVideo
		}
	}
	m := strings.ToLower(doc.MimeType)
	switch {
	case strings.HasPrefix(m, "image/"):
		return ai.MediaPhoto
	case strings.HasPrefix(m, "video/"):
		return ai.MediaVideo
	case strings.HasPrefix(m, "audio/"):
		return ai.MediaAudio
	}
	return ai.MediaDocument
}

// replyMoodSticker answers an incoming sticker the way IotaXMedia does:
// mood-matched pack reply, then learn-and-reply from the incoming pack, then any
// pack; finally it falls back to the bot's mood packs and the assistant's real
// auto-save pack.
func (c *Client) replyMoodSticker(ctx context.Context, m *tg.Message) {
	doc := messageDoc(m)
	if doc == nil {
		return
	}
	chatID := chatIDFromPeer(m.PeerID)
	wantMood := mood.FromEmoji(emojiFromDoc(doc))
	slog.Info("replyMoodSticker", "mood", wantMood, "hasStore", c.st != nil, "hasProvider", c.moodSticker != nil)

	if c.st != nil {
		if c.replyStickerForMood(ctx, chatID, wantMood, doc.ID) {
			return
		}
		if c.autoReplyFromPack(ctx, chatID, doc) {
			return
		}
		if c.replyAnySticker(ctx, chatID, doc.ID) {
			return
		}
	}

	if c.moodSticker != nil {
		if path, ok := c.moodSticker(chatID, wantMood); ok && path != "" {
			if err := c.SendMedia(chatID, store.Media{Kind: media.KindSticker, Source: path}); err != nil {
				slog.Warn("mood sticker send failed", "mood", wantMood, "err", err)
			} else {
				slog.Info("mood sticker sent", "mood", wantMood)
			}
			return
		}
		slog.Debug("moodSticker provider returned none; falling back")
	}

	if sd, err := c.RandomSticker(ctx, StickerStatic); err == nil && sd != nil {
		if err := c.SendSticker(chatID, sd); err != nil {
			slog.Warn("fallback sticker send failed", "err", err)
		} else {
			slog.Info("fallback sticker sent")
		}
	} else if err != nil {
		slog.Warn("fallback sticker lookup failed", "err", err)
	}
}

// replyMedia downloads an incoming media message, analyzes it with the
// multimodal model and replies with the result.
func (c *Client) replyMedia(ctx context.Context, m *tg.Message, kind string) {
	slog.Debug("replyMedia enter", "kind", kind, "ai", c.ai != nil)
	caption := strings.TrimSpace(m.Message)
	if strings.HasPrefix(caption, "/") || strings.HasPrefix(caption, ".") {
		slog.Debug("replyMedia skip: command caption")
		return
	}
	if c.ai == nil {
		slog.Debug("replyMedia skip: no ai")
		return
	}
	if c.albumSkip(m) {
		slog.Debug("replyMedia skip: album")
		return
	}
	if !c.acquireSlot() {
		slog.Debug("replyMedia skip: busy")
		c.sendToChat(ctx, m, mediaBusy)
		return
	}
	defer c.releaseSlot()
	slog.Debug("replyMedia slot acquired")

	stop := c.startTyping(ctx, m.PeerID)
	defer stop()

	data, mimeType, filename, err := c.downloadMedia(ctx, m, kind)
	if err != nil {
		slog.Warn("assistant media download failed", "kind", kind, "err", err)
		if errors.Is(err, ai.ErrMediaTooBig) {
			c.sendToChat(ctx, m, mediaTooBig)
		} else {
			c.sendToChat(ctx, m, mediaDownloadFail)
		}
		return
	}
	slog.Info("assistant media downloaded", "kind", kind, "mime", mimeType, "bytes", len(data), "filename", filename)

	chatID := chatIDFromPeer(m.PeerID)
	answer, err := c.ai.AnalyzeMedia(ctx, captionOr(caption), ai.MediaInput{
		Kind:     kind,
		MIME:     mimeType,
		Filename: filename,
		Data:     data,
		Context:  c.ai.RecentContext(ctx, ai.SessionKey(chatID), 8),
	})
	if err != nil || answer == "" {
		slog.Warn("assistant media analyze failed", "kind", kind, "err", errText(err))
		c.sendToChat(ctx, m, mediaAnalyzeFail)
		return
	}
	slog.Info("assistant media analyzed", "kind", kind, "chars", len(answer))
	c.ai.RememberTurn(ctx, ai.SessionKey(chatID), "[media "+kind+"] "+captionOr(caption), answer)
	c.sendToChat(ctx, m, answer)
}

// tryReplyToMedia mirrors IotaXMedia's _try_reply_to_media: when a text message
// is a reply to a media message, it re-downloads and re-analyzes that media with
// the user's text as the prompt. handled is true once the reply-to-media path is
// engaged; reply is the answer (empty when an error was already reported).
func (c *Client) tryReplyToMedia(ctx context.Context, m *tg.Message, text string) (reply string, handled bool) {
	if c.ai == nil {
		return "", false
	}
	rh, ok := m.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || rh.ReplyToMsgID <= 0 {
		return "", false
	}
	replied, err := c.fetchMessage(ctx, m.PeerID, rh.ReplyToMsgID)
	if err != nil || replied == nil {
		return "", false
	}
	kind, ok := messageMediaKind(replied)
	if !ok {
		return "", false
	}
	if !c.acquireSlot() {
		c.sendToChat(ctx, m, mediaBusy)
		return "", true
	}
	defer c.releaseSlot()

	stop := c.startTyping(ctx, m.PeerID)
	defer stop()

	data, mimeType, filename, err := c.downloadMedia(ctx, replied, kind)
	if err != nil {
		if errors.Is(err, ai.ErrMediaTooBig) {
			c.sendToChat(ctx, m, mediaTooBig)
		} else {
			c.sendToChat(ctx, m, mediaDownloadFail)
		}
		return "", true
	}
	answer, err := c.ai.AnalyzeMedia(ctx, captionOr(strings.TrimSpace(text)), ai.MediaInput{
		Kind:     kind,
		MIME:     mimeType,
		Filename: filename,
		Data:     data,
		Context:  c.ai.RecentContext(ctx, ai.SessionKey(chatIDFromPeer(m.PeerID)), 8),
	})
	if err != nil || answer == "" {
		slog.Warn("assistant reply-to-media analyze failed", "kind", kind, "err", errText(err))
		c.sendToChat(ctx, m, mediaAnalyzeFail)
		return "", true
	}
	return answer, true
}

// fetchMessage loads a single message by ID from the given peer's history.
func (c *Client) fetchMessage(ctx context.Context, peer tg.PeerClass, id int) (*tg.Message, error) {
	in, err := c.inputPeer(chatIDFromPeer(peer))
	if err != nil {
		return nil, err
	}
	ids := []tg.InputMessageClass{&tg.InputMessageID{ID: id}}
	var res tg.MessagesMessagesClass
	if ch, ok := in.(*tg.InputPeerChannel); ok {
		res, err = c.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
			ID:      ids,
		})
	} else {
		res, err = c.api.MessagesGetMessages(ctx, ids)
	}
	if err != nil {
		return nil, err
	}
	for _, mc := range messagesList(res) {
		if msg, ok := mc.(*tg.Message); ok && msg.ID == id {
			return msg, nil
		}
	}
	return nil, fmt.Errorf("message %d not found", id)
}

// messagesList flattens the message list of a MessagesMessagesClass result.
func messagesList(res tg.MessagesMessagesClass) []tg.MessageClass {
	switch v := res.(type) {
	case *tg.MessagesMessages:
		return v.Messages
	case *tg.MessagesMessagesSlice:
		return v.Messages
	case *tg.MessagesChannelMessages:
		return v.Messages
	}
	return nil
}

// analyzeIncomingMedia downloads and analyzes media, returning the model reply.
func (c *Client) analyzeIncomingMedia(ctx context.Context, m *tg.Message, kind, caption string) (string, error) {
	data, mimeType, filename, err := c.downloadMedia(ctx, m, kind)
	if err != nil {
		return "", err
	}
	return c.ai.AnalyzeMedia(ctx, captionOr(caption), ai.MediaInput{
		Kind:     kind,
		MIME:     mimeType,
		Filename: filename,
		Data:     data,
		Context:  c.ai.RecentContext(ctx, ai.SessionKey(chatIDFromPeer(m.PeerID)), 8),
	})
}

// downloadMedia streams a message's media into memory, returning the bytes and
// metadata. Per-kind size limits are enforced before the download starts.
func (c *Client) downloadMedia(ctx context.Context, m *tg.Message, kind string) ([]byte, string, string, error) {
	loc, size, mimeType, filename, ok := mediaLocation(m)
	if !ok {
		return nil, "", "", fmt.Errorf("no downloadable media")
	}
	if size > 0 && size > ai.SizeLimit(kind, mimeType, filename) {
		return nil, "", "", ai.ErrMediaTooBig
	}
	if size > maxMediaBytes {
		return nil, "", "", ai.ErrMediaTooBig
	}

	var buf bytes.Buffer
	if _, err := downloader.NewDownloader().Download(c.api, loc).Stream(ctx, &buf); err != nil {
		return nil, "", "", err
	}
	return buf.Bytes(), mimeType, filename, nil
}

// maxMediaBytes caps downloads at 25 MB so a stray large file can't blow memory.
const maxMediaBytes = 25 * 1024 * 1024

// mediaLocation resolves a downloadable location and metadata for a message.
func mediaLocation(m *tg.Message) (tg.InputFileLocationClass, int64, string, string, bool) {
	switch md := m.Media.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := md.Photo.(*tg.Photo)
		if !ok || photo == nil {
			return nil, 0, "", "", false
		}
		sizeType, size, ok := largestPhotoSize(photo)
		if !ok {
			return nil, 0, "", "", false
		}
		return photo.AsInputPhotoFileLocation(sizeType), size, "image/jpeg", "", true
	case *tg.MessageMediaDocument:
		doc, ok := md.Document.(*tg.Document)
		if !ok || doc == nil {
			return nil, 0, "", "", false
		}
		return doc.AsInputDocumentFileLocation(""), doc.Size, doc.MimeType, docFilename(doc), true
	}
	return nil, 0, "", "", false
}

// largestPhotoSize returns the largest downloadable photo size's type and size.
func largestPhotoSize(photo *tg.Photo) (string, int64, bool) {
	best := ""
	var bestSize int64
	for _, s := range photo.Sizes {
		switch sz := s.(type) {
		case *tg.PhotoSize:
			if int64(sz.Size) > bestSize {
				best, bestSize = sz.Type, int64(sz.Size)
			}
		case *tg.PhotoSizeProgressive:
			var max int
			for _, v := range sz.Sizes {
				if v > max {
					max = v
				}
			}
			if int64(max) > bestSize {
				best, bestSize = sz.Type, int64(max)
			}
		}
	}
	if best == "" {
		return "", 0, false
	}
	return best, bestSize, true
}

// docFilename extracts a document's file name attribute, if any.
func docFilename(doc *tg.Document) string {
	for _, a := range doc.Attributes {
		if fn, ok := a.(*tg.DocumentAttributeFilename); ok {
			return fn.FileName
		}
	}
	return ""
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// captionOr returns the caption, or the standard empty-caption placeholder.
func captionOr(caption string) string {
	if strings.TrimSpace(caption) == "" {
		return ai.EmptyCaption
	}
	return caption
}

// albumSkip returns true for duplicate photos in the same album (media group),
// so only the first is analyzed.
func (c *Client) albumSkip(m *tg.Message) bool {
	gid := m.GroupedID
	if gid == 0 {
		return false
	}
	key := fmt.Sprintf("%d", gid)
	now := time.Now()
	c.albumMu.Lock()
	defer c.albumMu.Unlock()
	if last, ok := c.albumSeen[key]; ok && now.Sub(last) < albumTTL {
		return true
	}
	c.albumSeen[key] = now
	if len(c.albumSeen) > 64 {
		for k, t := range c.albumSeen {
			if now.Sub(t) >= albumTTL {
				delete(c.albumSeen, k)
			}
		}
	}
	return false
}

func (c *Client) acquireSlot() bool {
	select {
	case c.mediaSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (c *Client) releaseSlot() {
	select {
	case <-c.mediaSlots:
	default:
	}
}
