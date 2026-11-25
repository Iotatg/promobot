package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/Iotatg/gotdbot"
)

// cmdSetString connects an assistant account using a Pyrogram session string,
// saving it for future restarts. Up to 5 accounts may be connected. The command
// message (which contains the secret) is deleted immediately.
func (b *Bot) cmdSetString(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	if b.sessions == nil {
		_, err := msg.ReplyText(c, "Session management is not available.", nil)
		return err
	}
	session := strings.TrimSpace(msg.Args())
	if session == "" {
		_, err := msg.ReplyText(c, "Usage: reply with /setstring <pyrogram string session>", nil)
		return err
	}

	// Remove the message containing the secret regardless of the outcome.
	_ = b.c.DeleteMessages(msg.ChatId, []int64{msg.Id}, nil)

	id, uid, err := b.sessions.AddSession(context.Background(), session)
	if err != nil {
		b.log.Warn("assistant session add failed", "err", err)
		_, rerr := msg.ReplyText(c, "Failed: "+err.Error(), nil)
		return rerr
	}
	b.log.Info("assistant account connected", "account", id, "user_id", uid)
	_, err = msg.ReplyText(c, fmt.Sprintf(
		"Account %s connected (user id %d). Use /accounts to list all connected accounts.",
		id, uid), nil)
	return err
}

// cmdDelString disconnects one account (by id) or every account when no id is
// given, and removes the stored session(s).
func (b *Bot) cmdDelString(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	if b.sessions == nil {
		_, err := msg.ReplyText(c, "Session management is not available.", nil)
		return err
	}
	id := strings.TrimSpace(msg.Args())
	if err := b.sessions.RemoveSession(context.Background(), id); err != nil {
		b.log.Warn("assistant session delete failed", "err", err)
		_, rerr := msg.ReplyText(c, "Failed: "+err.Error(), nil)
		return rerr
	}
	if id == "" {
		b.log.Info("all assistant accounts removed")
		_, err := msg.ReplyText(c, "All assistant accounts disconnected and removed.", nil)
		return err
	}
	b.log.Info("assistant account removed", "account", id)
	_, err := msg.ReplyText(c, "Account "+id+" disconnected and removed.", nil)
	return err
}

// cmdAccounts lists the connected assistant accounts.
func (b *Bot) cmdAccounts(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	if b.sessions == nil {
		_, err := msg.ReplyText(c, "Session management is not available.", nil)
		return err
	}
	lines := b.sessions.AccountLines()
	if len(lines) == 0 {
		_, err := msg.ReplyText(c, "No assistant accounts connected. Use /setstring <session>.", nil)
		return err
	}
	_, err := msg.ReplyText(c, "Connected accounts (max 5):\n"+strings.Join(lines, "\n"), nil)
	return err
}
