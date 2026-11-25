package bot

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"

	"github.com/Iotatg/gotdbot"

	"promobot/internal/media"
	"promobot/internal/mood"
)

// cmdAddSticker stores the replied-to sticker under a mood pack.
func (b *Bot) cmdAddSticker(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	replyID := msg.ReplyToMessageID()
	if replyID == 0 {
		_, err := msg.ReplyText(c, "Reply to a sticker with /addsticker <mood>.", nil)
		return err
	}
	replied, err := b.c.GetMessage(msg.ChatId, replyID)
	if err != nil || replied.Sticker() == nil {
		_, rerr := msg.ReplyText(c, "The replied message is not a sticker.", nil)
		return rerr
	}
	fileID := media.StickerRemoteID(replied)
	if fileID == "" {
		_, rerr := msg.ReplyText(c, "Could not read the sticker file id.", nil)
		return rerr
	}

	packArg := strings.TrimSpace(msg.Args())
	var pack string
	if packArg != "" {
		pack = mood.Sanitize(packArg)
	} else {
		pack = mood.FromEmoji(replied.Sticker().Emoji)
	}

	senderID, _ := gotdbot.SenderUserID(msg.SenderId)
	if err := b.store.AddStickerToPack(context.Background(), pack, fileID, senderID); err != nil {
		_, rerr := msg.ReplyText(c, "Save failed: "+err.Error(), nil)
		return rerr
	}
	b.log.Info("sticker added", "mood", pack)
	_, err = msg.ReplyText(c, fmt.Sprintf("Added sticker to mood pack '%s'.", pack), nil)
	return err
}

// cmdStickerPacks lists all mood packs and the assistant's auto-save packs.
func (b *Bot) cmdStickerPacks(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	packs, err := b.store.ListStickerPacks(context.Background())
	if err != nil {
		_, rerr := msg.ReplyText(c, "Failed to load packs: "+err.Error(), nil)
		return rerr
	}
	var sb strings.Builder
	sb.WriteString("Mood packs (stored by the bot):\n")
	if len(packs) == 0 {
		sb.WriteString("- none yet. Reply to a sticker with /addsticker <mood>.\n")
	}
	for mood, n := range packs {
		sb.WriteString(fmt.Sprintf("- %s: %d\n", mood, n))
	}
	if b.assistant != nil {
		auto := b.assistant.AutoStickerPacks()
		sb.WriteString("\nAuto-save packs (on Telegram, by the assistant):\n")
		if len(auto) == 0 {
			sb.WriteString("- none yet. Send a sticker to the assistant or use /kang.\n")
		}
		for _, line := range auto {
			sb.WriteString(line + "\n")
		}
	}
	_, err = msg.ReplyText(c, sb.String(), nil)
	return err
}

// cmdClearStickers removes a mood pack.
func (b *Bot) cmdClearStickers(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	pack := mood.Sanitize(strings.TrimSpace(msg.Args()))
	if strings.TrimSpace(msg.Args()) == "" {
		_, err := msg.ReplyText(c, "Usage: /clearstickers <mood>", nil)
		return err
	}
	n, err := b.store.ClearStickerPack(context.Background(), pack)
	if err != nil {
		_, rerr := msg.ReplyText(c, "Failed: "+err.Error(), nil)
		return rerr
	}
	b.log.Info("sticker pack cleared", "mood", pack, "removed", n)
	_, err = msg.ReplyText(c, fmt.Sprintf("Cleared %d sticker(s) from '%s'.", n, pack), nil)
	return err
}

// cmdSticker sends a random sticker from a mood pack.
func (b *Bot) cmdSticker(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	pack := mood.Sanitize(strings.TrimSpace(msg.Args()))
	if strings.TrimSpace(msg.Args()) == "" {
		_, err := msg.ReplyText(c, "Usage: /sticker <mood>", nil)
		return err
	}
	ids, err := b.store.GetStickersForMood(context.Background(), pack)
	if err != nil {
		_, rerr := msg.ReplyText(c, "Failed: "+err.Error(), nil)
		return rerr
	}
	ids = botRefs(ids)
	if len(ids) == 0 {
		_, rerr := msg.ReplyText(c, fmt.Sprintf("Mood pack '%s' has no bot-sendable stickers (only assistant references).", pack), nil)
		return rerr
	}
	pick := ids[rand.Intn(len(ids))]
	_ = b.c.SendChooseSticker(msg.ChatId)
	_, err = b.c.SendSticker(msg.ChatId, gotdbot.FileFromRemote(pick), nil)
	return err
}

// cmdAddPack imports every sticker from the replied sticker's whole set into a
// mood pack, mirroring IotaXMedia's /addlearning-sticker bulk learn.
func (b *Bot) cmdAddPack(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	replyID := msg.ReplyToMessageID()
	if replyID == 0 {
		_, err := msg.ReplyText(c, "Reply to any sticker from a pack with /addpack [mood].", nil)
		return err
	}
	replied, err := b.c.GetMessage(msg.ChatId, replyID)
	if err != nil || replied.Sticker() == nil {
		_, rerr := msg.ReplyText(c, "The replied message is not a sticker.", nil)
		return rerr
	}
	setID := replied.Sticker().SetId
	if setID == 0 {
		_, rerr := msg.ReplyText(c, "That sticker is not part of a pack; use /addsticker instead.", nil)
		return rerr
	}
	set, err := b.c.GetStickerSet(setID)
	if err != nil || set == nil {
		_, rerr := msg.ReplyText(c, "Could not load the sticker pack.", nil)
		return rerr
	}

	packArg := strings.TrimSpace(msg.Args())
	auto := packArg == ""
	pack := mood.Sanitize(packArg)
	if auto {
		pack = autoMoodFromBotSet(set)
	}

	senderID, _ := gotdbot.SenderUserID(msg.SenderId)
	ctx := context.Background()
	added := 0
	for _, s := range set.Stickers {
		if s.Sticker == nil || s.Sticker.Remote == nil {
			continue
		}
		fid := s.Sticker.Remote.Id
		if fid == "" {
			fid = s.Sticker.Remote.UniqueId
		}
		if fid == "" {
			continue
		}
		if err := b.store.AddStickerToPack(ctx, pack, fid, senderID); err == nil {
			added++
		}
	}
	total, _ := b.store.GetStickersForMood(ctx, pack)
	label := ""
	if auto {
		label = " (auto mood)"
	}
	b.log.Info("sticker pack imported", "mood", pack, "added", added)
	_, err = msg.ReplyText(c, fmt.Sprintf("Imported %d sticker(s) from \"%s\" into pack '%s'%s (total %d).", added, set.Title, pack, label, len(total)), nil)
	return err
}

// cmdPreviewSticker sends one sticker from a mood pack, mirroring IotaXMedia's
// /preview-sticker.
func (b *Bot) cmdPreviewSticker(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	pack := mood.Sanitize(strings.TrimSpace(msg.Args()))
	if strings.TrimSpace(msg.Args()) == "" {
		_, err := msg.ReplyText(c, "Usage: /previewsticker <mood>", nil)
		return err
	}
	ids, err := b.store.GetStickersForMood(context.Background(), pack)
	if err != nil {
		_, rerr := msg.ReplyText(c, "Failed: "+err.Error(), nil)
		return rerr
	}
	ids = botRefs(ids)
	if len(ids) == 0 {
		_, rerr := msg.ReplyText(c, fmt.Sprintf("Mood pack '%s' is empty.", pack), nil)
		return rerr
	}
	pick := ids[rand.Intn(len(ids))]
	_ = b.c.SendChooseSticker(msg.ChatId)
	if _, err := b.c.SendSticker(msg.ChatId, gotdbot.FileFromRemote(pick), nil); err != nil {
		return err
	}
	_, err = msg.ReplyText(c, fmt.Sprintf("Preview of '%s' (1 of %d).", pack, len(ids)), nil)
	return err
}

// botRefs keeps only TDLib (bot-sendable) file ids, dropping assistant "mt:" refs.
func botRefs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !strings.HasPrefix(id, "mt:") {
			out = append(out, id)
		}
	}
	return out
}

// autoMoodFromBotSet picks the dominant emoji mood from a pack, falling back to
// a slug of the pack title.
func autoMoodFromBotSet(set *gotdbot.StickerSet) string {
	tally := make(map[string]int)
	for _, s := range set.Stickers {
		if m := mood.FromEmoji(s.Emoji); m != mood.Default {
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
	return mood.Sanitize(set.Title)
}

// cmdKang saves the replied sticker into a real Telegram sticker pack owned by
// the assistant (like IotaXMedia's /kang).
func (b *Bot) cmdKang(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	if b.assistant == nil {
		_, err := msg.ReplyText(c, "Assistant is not connected, so new sticker packs can't be created.", nil)
		return err
	}
	replyID := msg.ReplyToMessageID()
	if replyID == 0 {
		_, err := msg.ReplyText(c, "Reply to a sticker with /kang [emoji] to save it into a sticker pack.", nil)
		return err
	}
	replied, err := b.c.GetMessage(msg.ChatId, replyID)
	if err != nil || replied.Sticker() == nil {
		_, rerr := msg.ReplyText(c, "The replied message is not a sticker.", nil)
		return rerr
	}
	st := replied.Sticker()

	file, err := replied.DownloadSync(b.c)
	if err != nil || file == nil || file.Local == nil || file.Local.Path == "" {
		_, rerr := msg.ReplyText(c, "Could not download the sticker.", nil)
		return rerr
	}
	data, err := os.ReadFile(file.Local.Path)
	if err != nil {
		_, rerr := msg.ReplyText(c, "Could not read the downloaded sticker: "+err.Error(), nil)
		return rerr
	}

	name, mime, emoji := stickerMeta(st)
	if override := strings.TrimSpace(msg.Args()); override != "" {
		emoji = override
	}
	summary, err := b.assistant.KangStickerFile(context.Background(), name, data, mime, emoji)
	if err != nil {
		_, rerr := msg.ReplyText(c, "Kang failed: "+err.Error(), nil)
		return rerr
	}
	b.log.Info("sticker kanged", "pack", summary)
	_, err = msg.ReplyText(c, "Saved!\n"+summary, nil)
	return err
}

// stickerMeta maps a TDLib sticker to a file name, MIME type and emoji.
func stickerMeta(st *gotdbot.Sticker) (name, mime, emoji string) {
	emoji = st.Emoji
	switch st.Format.(type) {
	case gotdbot.StickerFormatTgs:
		return "sticker.tgs", "application/x-tgsticker", emoji
	case gotdbot.StickerFormatWebm:
		return "sticker.webm", "video/webm", emoji
	default:
		return "sticker.webp", "image/webp", emoji
	}
}
