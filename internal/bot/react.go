package bot

import (
	"context"
	"strconv"
	"strings"

	"github.com/Iotatg/gotdbot"

	"promobot/internal/assistant"
)

// cmdReactMode shows or sets the assistant reaction decision mode:
// offline (keyword engine), ai (model decides) or both (offline then ai).
func (b *Bot) cmdReactMode(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	if b.sessions == nil {
		_, err := msg.ReplyText(c, "Assistant is not available.", nil)
		return err
	}
	arg := strings.ToLower(strings.TrimSpace(msg.Args()))
	switch arg {
	case "":
		_, err := msg.ReplyText(c, "Reaction mode: "+b.sessions.ReactMode()+
			"\nSet with /reactmode offline|ai|both", nil)
		return err
	case assistant.ReactModeOffline, assistant.ReactModeAI, assistant.ReactModeBoth:
		b.sessions.SetReactMode(arg)
		b.log.Info("reaction mode changed", "mode", arg)
		_, err := msg.ReplyText(c, "Reaction mode set to "+arg+".", nil)
		return err
	default:
		_, err := msg.ReplyText(c, "Usage: /reactmode offline|ai|both", nil)
		return err
	}
}

// cmdClearMemory clears every user's stored conversation for all connected
// assistant accounts (owner only). /clearmemorys is an alias.
func (b *Bot) cmdClearMemory(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	if b.sessions == nil {
		_, err := msg.ReplyText(c, "Assistant is not available.", nil)
		return err
	}
	n, err := b.sessions.ClearAllMemory(context.Background())
	if err != nil {
		b.log.Warn("clear memory failed", "err", err)
		_, rerr := msg.ReplyText(c, "Failed: "+err.Error(), nil)
		return rerr
	}
	b.log.Info("assistant memory cleared", "docs", n)
	_, err = msg.ReplyText(c, "Cleared all assistant memory ("+strconv.FormatInt(n, 10)+" conversation(s)).", nil)
	return err
}
