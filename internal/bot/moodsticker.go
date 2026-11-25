package bot

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/Iotatg/gotdbot"

	"promobot/internal/mood"
	"promobot/internal/store"
)

// MoodStickerProvider returns a local path to a sticker matching the given chat
// and mood, using the bot's /addsticker mood packs. It tries the exact mood
// first, then related moods, then any pack. Stickers are cycled per chat so the
// same sticker is never sent twice in a row.
func (b *Bot) MoodStickerProvider(chatID int64, wantMood string) (string, bool) {
	ctx := context.Background()
	remoteID := b.pickMoodSticker(ctx, chatID, wantMood)
	if remoteID == "" {
		remoteID = b.pickAnySticker(ctx, chatID)
	}
	if remoteID == "" {
		return "", false
	}
	path, err := b.stickerPath(ctx, remoteID)
	if err != nil {
		b.log.Warn("mood sticker download failed", "err", err)
		return "", false
	}
	return path, true
}

// pickMoodSticker returns the next sticker from the first non-empty mood pack
// among the candidate moods (cycling per chat).
func (b *Bot) pickMoodSticker(ctx context.Context, chatID int64, wantMood string) string {
	for _, m := range mood.Candidates(wantMood) {
		ids, err := b.store.GetStickersForMood(ctx, m)
		if err != nil || len(ids) == 0 {
			continue
		}
		return b.cyclePick(fmt.Sprintf("%d|%s", chatID, m), ids)
	}
	return ""
}

// pickAnySticker returns the next sticker from any mood pack, used when no mood
// pack matches (mirrors IotaXMedia's _reply_any_sticker).
func (b *Bot) pickAnySticker(ctx context.Context, chatID int64) string {
	packs, err := b.store.ListStickerPacks(ctx)
	if err != nil {
		return ""
	}
	var all []string
	for m := range packs {
		ids, err := b.store.GetStickersForMood(ctx, m)
		if err != nil {
			continue
		}
		all = append(all, ids...)
	}
	if len(all) == 0 {
		return ""
	}
	return b.cyclePick(fmt.Sprintf("%d|*", chatID), all)
}

// cyclePick returns the next sticker from a shuffled pool keyed by scope. Once
// the pool is exhausted it is refilled and reshuffled, and the first pick is
// swapped if it would repeat the last sticker sent for this scope.
func (b *Bot) cyclePick(scope string, ids []string) string {
	b.stickerMu.Lock()
	defer b.stickerMu.Unlock()
	if b.stickerCycles == nil {
		b.stickerCycles = make(map[string][]string)
	}
	if b.stickerLast == nil {
		b.stickerLast = make(map[string]string)
	}
	pool := b.stickerCycles[scope]
	if len(pool) == 0 {
		pool = append([]string(nil), ids...)
		rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		if last := b.stickerLast[scope]; last != "" && len(pool) > 1 && pool[0] == last {
			pool[0], pool[1] = pool[1], pool[0]
		}
	}
	pick := pool[0]
	b.stickerCycles[scope] = pool[1:]
	b.stickerLast[scope] = pick
	return pick
}

// stickerPath downloads a sticker by remote file id once and caches the path.
func (b *Bot) stickerPath(ctx context.Context, remoteID string) (string, error) {
	b.stickerMu.Lock()
	if p, ok := b.stickerCache[remoteID]; ok && p != "" {
		b.stickerMu.Unlock()
		return p, nil
	}
	b.stickerMu.Unlock()

	path, err := b.downloadStickerToCache(ctx, remoteID)
	if err != nil {
		return "", err
	}
	b.stickerMu.Lock()
	if b.stickerCache == nil {
		b.stickerCache = make(map[string]string)
	}
	b.stickerCache[remoteID] = path
	b.stickerMu.Unlock()
	return path, nil
}

// downloadStickerToCache downloads a sticker by remote file id and copies it
// into MediaDir, returning the stable local path.
func (b *Bot) downloadStickerToCache(ctx context.Context, remoteID string) (string, error) {
	rf, err := b.c.GetRemoteFile(remoteID, &gotdbot.GetRemoteFileOpts{})
	if err != nil {
		return "", err
	}
	file, err := b.c.DownloadFileProgress(rf.Id, nil)
	if err != nil {
		return "", err
	}
	if file == nil || file.Local == nil || file.Local.Path == "" {
		return "", fmt.Errorf("sticker has no local path after download")
	}

	if err := os.MkdirAll(b.cfg.MediaDir, 0o755); err != nil {
		return "", err
	}
	data, err := os.ReadFile(file.Local.Path)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(b.cfg.MediaDir, "mood-"+store.NewID()+filepath.Ext(file.Local.Path))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return "", err
	}
	return dst, nil
}
