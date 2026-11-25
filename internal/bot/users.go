package bot

import (
	"context"
	"fmt"

	"github.com/Iotatg/gotdbot"

	"promobot/internal/store"
)

// onTrackUser records the sender of any private message as a "served" user so
// they can receive /broadcast -user direct messages. It runs in an earlier
// handler group and continues to the normal handlers afterwards.
func (b *Bot) onTrackUser(c *gotdbot.Client, msg *gotdbot.Message) error {
	if uid, ok := gotdbot.SenderUserID(msg.SenderId); ok && uid != 0 {
		if err := b.store.UpsertUser(context.Background(), store.User{UserID: uid}); err != nil {
			b.log.Warn("track user failed", "user", uid, "err", err.Error())
		}
	}
	return gotdbot.ContinueGroups
}

// cmdUsers reports how many users the bot has served (can be reached with
// /broadcast -user).
func (b *Bot) cmdUsers(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	count, err := b.store.CountUsers(context.Background())
	if err != nil {
		_, rerr := msg.ReplyText(c, "Failed to count users: "+err.Error(), nil)
		return rerr
	}
	_, err = msg.ReplyText(c, fmt.Sprintf("Served users: %d\nUse /broadcast -user to message them.", count), nil)
	return err
}
