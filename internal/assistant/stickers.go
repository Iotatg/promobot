package assistant

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// StickerKind identifies the format of a sticker pack.
type StickerKind int

const (
	StickerStatic StickerKind = iota
	StickerAnimated
	StickerVideo
)

// String returns the short name used in pack short names and messages.
func (k StickerKind) String() string {
	switch k {
	case StickerAnimated:
		return "animated"
	case StickerVideo:
		return "video"
	default:
		return "static"
	}
}

// Title returns a human friendly kind label.
func (k StickerKind) Title() string {
	switch k {
	case StickerAnimated:
		return "Animated"
	case StickerVideo:
		return "Video"
	default:
		return "Sticker"
	}
}

// limit is the maximum number of stickers Telegram allows per pack.
func (k StickerKind) limit() int {
	if k == StickerStatic {
		return 120
	}
	return 50
}

// PackInfo describes a sticker pack on Telegram.
type PackInfo struct {
	ShortName string
	Title     string
	Index     int
	Count     int
	Kind      StickerKind
	Created   bool
	Duplicate bool
}

func (p *PackInfo) link() string { return "https://t.me/addstickers/" + p.ShortName }

// summary is a short human readable line describing the result.
func (p *PackInfo) summary() string {
	verb := "Added to"
	if p.Created {
		verb = "Created"
	} else if p.Duplicate {
		verb = "Already in"
	}
	return fmt.Sprintf("%s %s %s pack (%d sticker(s)): %s", verb, p.Title, p.Kind, p.Count, p.link())
}

// SaveStickerDocument adds an existing sticker document to the assistant's
// auto-save pack, creating the pack on first use. It is safe to call with a
// sticker the assistant has just received: the document reference is reused.
func (c *Client) SaveStickerDocument(ctx context.Context, doc *tg.Document, emoji string) (*PackInfo, error) {
	if doc == nil {
		return nil, fmt.Errorf("nil sticker document")
	}
	if strings.TrimSpace(emoji) == "" {
		emoji = emojiFromDoc(doc)
	}
	if strings.TrimSpace(emoji) == "" {
		emoji = "🤔"
	}
	item := tg.InputStickerSetItem{
		Document: &tg.InputDocument{
			ID:            doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: doc.FileReference,
		},
		Emoji: emoji,
	}
	info, err := c.addOrcreate(ctx, item, kindFromMime(doc.MimeType), doc.ID)
	if err == nil {
		c.rememberPack(info)
	}
	return info, err
}

// SendSticker sends an existing sticker document to a chat as the assistant.
func (c *Client) SendSticker(chatID int64, doc *tg.Document) error {
	if doc == nil {
		return fmt.Errorf("nil sticker document")
	}
	peer, err := c.inputPeer(chatID)
	if err != nil {
		return err
	}
	c.chooseSticker(context.Background(), peer)
	_, err = message.NewSender(c.api).To(peer).Document(context.Background(), doc)
	return err
}

// RandomSticker returns a random sticker from the assistant's auto-save packs of
// the given kind.
func (c *Client) RandomSticker(ctx context.Context, kind StickerKind) (*tg.Document, error) {
	for idx := 0; idx < 100; idx++ {
		set, err := c.getSet(ctx, c.packShortName(kind, idx))
		if err != nil {
			break
		}
		var docs []*tg.Document
		for _, d := range set.Documents {
			if doc, ok := d.(*tg.Document); ok {
				docs = append(docs, doc)
			}
		}
		if len(docs) > 0 {
			return docs[rand.Intn(len(docs))], nil
		}
	}
	return nil, fmt.Errorf("no %s stickers saved yet", kind)
}

// KangStickerFile uploads raw sticker bytes as the assistant and adds them to
// the auto-save pack. It returns a short summary line describing the result.
func (c *Client) KangStickerFile(ctx context.Context, name string, data []byte, mime, emoji string) (string, error) {
	info, err := c.kangStickerFile(ctx, name, data, mime, emoji)
	if err != nil {
		return "", err
	}
	return info.summary(), nil
}

func (c *Client) kangStickerFile(ctx context.Context, name string, data []byte, mime, emoji string) (*PackInfo, error) {
	if strings.TrimSpace(emoji) == "" {
		emoji = "🤔"
	}
	up := uploader.NewUploader(c.api)
	file, err := up.FromBytes(ctx, name, data)
	if err != nil {
		return nil, fmt.Errorf("upload sticker: %w", err)
	}
	media := &tg.InputMediaUploadedDocument{
		File:     file,
		MimeType: mime,
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: name},
			&tg.DocumentAttributeSticker{Alt: emoji, Stickerset: &tg.InputStickerSetEmpty{}},
		},
	}
	res, err := c.api.MessagesUploadMedia(ctx, &tg.MessagesUploadMediaRequest{
		Peer:  &tg.InputPeerSelf{},
		Media: media,
	})
	if err != nil {
		return nil, fmt.Errorf("upload media: %w", err)
	}
	doc := docFromMedia(res)
	if doc == nil {
		return nil, fmt.Errorf("upload returned no document")
	}
	info, err := c.SaveStickerDocument(ctx, doc, emoji)
	if err != nil {
		return nil, err
	}
	return info, nil
}

// addOrcreate adds the item to the first pack that has room, creating packs as
// needed and rolling over to the next index when a pack is full.
func (c *Client) addOrcreate(ctx context.Context, item tg.InputStickerSetItem, kind StickerKind, docID int64) (*PackInfo, error) {
	for idx := 0; ; idx++ {
		short := c.packShortName(kind, idx)
		set, err := c.getSet(ctx, short)
		if err != nil {
			if !tgerr.Is(err, "STICKERSET_INVALID") {
				return nil, err
			}
			res, cerr := c.api.StickersCreateStickerSet(ctx, &tg.StickersCreateStickerSetRequest{
				UserID:    &tg.InputUserSelf{},
				Title:     c.packTitle(kind, idx),
				ShortName: short,
				Stickers:  []tg.InputStickerSetItem{item},
			})
			if cerr == nil {
				return packFromResult(res, kind, idx, true, false), nil
			}
			if tgerr.Is(cerr, "PACK_SHORT_NAME_OCCUPIED", "PACK_TITLE_INVALID") {
				continue
			}
			return nil, cerr
		}

		if docInSet(set, docID) {
			return &PackInfo{
				ShortName: set.Set.ShortName,
				Title:     set.Set.Title,
				Index:     idx,
				Count:     set.Set.Count,
				Kind:      kind,
				Duplicate: true,
			}, nil
		}
		if len(set.Documents) >= kind.limit() {
			continue
		}

		res, aerr := c.api.StickersAddStickerToSet(ctx, &tg.StickersAddStickerToSetRequest{
			Stickerset: &tg.InputStickerSetShortName{ShortName: short},
			Sticker:    item,
		})
		if aerr != nil {
			if tgerr.Is(aerr, "STICKERS_TOO_MUCH", "PACK_STICKERS_TOO_MUCH", "STICKERSET_INVALID") {
				continue
			}
			return nil, aerr
		}
		return packFromResult(res, kind, idx, false, false), nil
	}
}

func (c *Client) getSet(ctx context.Context, short string) (*tg.MessagesStickerSet, error) {
	res, err := c.api.MessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{
		Stickerset: &tg.InputStickerSetShortName{ShortName: short},
	})
	if err != nil {
		return nil, err
	}
	set, ok := res.(*tg.MessagesStickerSet)
	if !ok {
		return nil, fmt.Errorf("unexpected sticker set type %T", res)
	}
	return set, nil
}

func packFromResult(res tg.MessagesStickerSetClass, kind StickerKind, idx int, created, dup bool) *PackInfo {
	pi := &PackInfo{Kind: kind, Index: idx, Created: created, Duplicate: dup}
	if s, ok := res.(*tg.MessagesStickerSet); ok {
		pi.ShortName = s.Set.ShortName
		pi.Title = s.Set.Title
		pi.Count = s.Set.Count
		if n := len(s.Documents); n > 0 {
			pi.Count = n
		}
	}
	return pi
}

func (c *Client) packShortName(kind StickerKind, idx int) string {
	name := fmt.Sprintf("radhika%d_%s", c.selfID, kind)
	if idx > 0 {
		name += strconv.Itoa(idx)
	}
	return name
}

func (c *Client) packTitle(kind StickerKind, idx int) string {
	title := fmt.Sprintf("Radhika's %s Pack", kind.Title())
	if idx > 0 {
		title += " " + strconv.Itoa(idx+1)
	}
	return title
}

// rememberPack keeps an in-memory snapshot so the bot can list auto-save packs.
func (c *Client) rememberPack(info *PackInfo) {
	if info == nil || info.ShortName == "" {
		return
	}
	c.packsMu.Lock()
	if c.autoPacks == nil {
		c.autoPacks = make(map[string]*PackInfo)
	}
	cp := *info
	c.autoPacks[info.ShortName] = &cp
	c.packsMu.Unlock()
}

// AutoStickerPacks returns a snapshot of the auto-save packs as display lines.
func (c *Client) AutoStickerPacks() []string {
	c.packsMu.Lock()
	defer c.packsMu.Unlock()
	out := make([]string, 0, len(c.autoPacks))
	for _, p := range c.autoPacks {
		out = append(out, fmt.Sprintf("- %s pack: %d sticker(s) → %s", p.Kind, p.Count, p.link()))
	}
	return out
}

// WarmAutoPacks loads the assistant's existing auto-save packs into memory so
// they can be listed after a restart. Packs fill sequentially, so scanning
// stops at the first missing index for each kind.
func (c *Client) WarmAutoPacks(ctx context.Context) {
	for _, kind := range []StickerKind{StickerStatic, StickerAnimated, StickerVideo} {
		for idx := 0; idx < 100; idx++ {
			set, err := c.getSet(ctx, c.packShortName(kind, idx))
			if err != nil {
				break
			}
			c.rememberPack(&PackInfo{
				ShortName: set.Set.ShortName,
				Title:     set.Set.Title,
				Index:     idx,
				Count:     len(set.Documents),
				Kind:      kind,
			})
			slog.Info("loaded auto-save sticker pack", "short_name", set.Set.ShortName, "count", len(set.Documents), "kind", kind.String())
		}
	}
}

func kindFromMime(mime string) StickerKind {
	mime = strings.ToLower(mime)
	switch {
	case strings.Contains(mime, "tgsticker"):
		return StickerAnimated
	case strings.Contains(mime, "webm"):
		return StickerVideo
	default:
		return StickerStatic
	}
}

func emojiFromDoc(doc *tg.Document) string {
	if doc == nil {
		return ""
	}
	for _, a := range doc.Attributes {
		if s, ok := a.(*tg.DocumentAttributeSticker); ok {
			return s.Alt
		}
	}
	return ""
}

func docInSet(set *tg.MessagesStickerSet, docID int64) bool {
	for _, d := range set.Documents {
		if doc, ok := d.(*tg.Document); ok && doc.ID == docID {
			return true
		}
	}
	return false
}

func docFromMedia(res tg.MessageMediaClass) *tg.Document {
	md, ok := res.(*tg.MessageMediaDocument)
	if !ok {
		return nil
	}
	doc, _ := md.Document.(*tg.Document)
	return doc
}
