package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Iotatg/gotdbot"
)

// cmdDelChannel unregisters a chat from the bot's allowlist. It stops all posting
// to that chat but does not make any account leave it.
func (b *Bot) cmdDelChannel(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	raw := strings.TrimSpace(msg.Args())
	if raw == "" {
		_, err := msg.ReplyText(c, "Usage: /delchannel <chat_id> (see /channels)", nil)
		return err
	}
	id, err := strconv.ParseInt(strings.Fields(raw)[0], 10, 64)
	if err != nil {
		_, rerr := msg.ReplyText(c, "Give the numeric chat id shown by /channels.", nil)
		return rerr
	}
	ctx := context.Background()
	if _, err := b.store.GetChannel(ctx, id); err != nil {
		_, rerr := msg.ReplyText(c, "That chat is not registered.", nil)
		return rerr
	}
	if err := b.store.DeleteChannel(ctx, id); err != nil {
		_, rerr := msg.ReplyText(c, "Remove failed: "+err.Error(), nil)
		return rerr
	}
	n, _ := b.store.DeleteSchedulesByChatID(ctx, id)
	b.log.Info("channel removed", "chat", id, "schedules", n)
	_, err = msg.ReplyText(c, fmt.Sprintf("Unregistered %d (removed %d scheduled post(s)).", id, n), nil)
	return err
}

// cmdLeave makes the assistant leave exactly one explicitly named, registered
// chat. It is intentionally single-chat and allowlist-scoped: no bulk leaving.
func (b *Bot) cmdLeave(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	if b.assistant == nil {
		_, err := msg.ReplyText(c, "Assistant is not connected.", nil)
		return err
	}
	raw := strings.TrimSpace(msg.Args())
	if raw == "" {
		_, err := msg.ReplyText(c, "Usage: /leave <chat_id> - assistant leaves that ONE registered chat.", nil)
		return err
	}
	id, err := strconv.ParseInt(strings.Fields(raw)[0], 10, 64)
	if err != nil {
		_, rerr := msg.ReplyText(c, "Give the numeric chat id shown by /channels.", nil)
		return rerr
	}
	ctx := context.Background()
	if _, err := b.store.GetChannel(ctx, id); err != nil {
		_, rerr := msg.ReplyText(c, "Only a registered chat can be left. See /channels.", nil)
		return rerr
	}
	if err := b.assistant.LeaveChat(id); err != nil {
		_, rerr := msg.ReplyText(c, "Leave failed: "+err.Error(), nil)
		return rerr
	}
	_ = b.store.DeleteChannel(ctx, id)
	_, _ = b.store.DeleteSchedulesByChatID(ctx, id)
	b.log.Info("assistant left chat", "chat", id)
	_, err = msg.ReplyText(c, fmt.Sprintf("Assistant left and unregistered %d.", id), nil)
	return err
}
