package bot

import (
	"context"
	"strings"
	"time"

	"github.com/Iotatg/gotdbot"

	"promobot/internal/ai"
)

// cmdAsk answers a one-off prompt with the configured LLM.
func (b *Bot) cmdAsk(c *gotdbot.Client, msg *gotdbot.Message) error {
	allowed := b.isOwner(msg)
	if !allowed {
		allowed, _ = b.store.IsChatbotEnabled(context.Background(), msg.ChatId)
	}
	if !allowed {
		return nil
	}
	prompt := strings.TrimSpace(msg.Args())
	if prompt == "" && msg.ReplyToMessageID() != 0 {
		if replied, err := b.c.GetMessage(msg.ChatId, msg.ReplyToMessageID()); err == nil {
			prompt = strings.TrimSpace(replied.GetText())
		}
	}
	if prompt == "" {
		_, err := msg.ReplyText(c, "Usage: /ask <your question> (or reply to a message with /ask)", nil)
		return err
	}
	b.replyAI(c, msg, prompt)
	return nil
}

// cmdChat toggles mention-based auto replies for the current chat (owner only).
func (b *Bot) cmdChat(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	arg := strings.ToLower(strings.TrimSpace(msg.Args()))
	ctx := context.Background()

	switch arg {
	case "on":
		if err := b.store.SetChatbotEnabled(ctx, msg.ChatId, true); err != nil {
			_, _ = msg.ReplyText(c, "Failed: "+err.Error(), nil)
			return nil
		}
		_, err := msg.ReplyText(c, "Chatbot enabled here. I'll reply when @mentioned or replied to.", nil)
		return err
	case "off":
		if err := b.store.SetChatbotEnabled(ctx, msg.ChatId, false); err != nil {
			_, _ = msg.ReplyText(c, "Failed: "+err.Error(), nil)
			return nil
		}
		_, err := msg.ReplyText(c, "Chatbot disabled here.", nil)
		return err
	default:
		enabled, _ := b.store.IsChatbotEnabled(ctx, msg.ChatId)
		state := "disabled"
		if enabled {
			state = "enabled"
		}
		_, err := msg.ReplyText(c, "Chatbot is "+state+" in this chat. Use /chat on|off.", nil)
		return err
	}
}

// cmdModel changes the active model (owner only).
func (b *Bot) cmdModel(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	arg := strings.TrimSpace(msg.Args())
	if arg == "" {
		_, err := msg.ReplyText(c, "Current model: "+b.ai.Model()+"\nUsage: /model <name>", nil)
		return err
	}
	b.ai.SetModel(arg)
	_, err := msg.ReplyText(c, "Model set to "+b.ai.Model(), nil)
	return err
}

// onAutoReply answers messages in chats where the chatbot is enabled and the
// bot is @mentioned or replied to. It never replies to every message.
func (b *Bot) onAutoReply(c *gotdbot.Client, msg *gotdbot.Message) error {
	if b.ai == nil || msg == nil || msg.IsOutgoing {
		return nil
	}
	if msg.IsCommand() {
		return nil
	}
	enabled, err := b.store.IsChatbotEnabled(context.Background(), msg.ChatId)
	if err != nil || !enabled {
		return nil
	}

	text := strings.TrimSpace(msg.GetText())
	if text == "" {
		return nil
	}

	username := b.botUsername()
	triggered := false
	if username != "" && strings.Contains(strings.ToLower(text), "@"+strings.ToLower(username)) {
		triggered = true
	}
	if !triggered && msg.ReplyToMessageID() != 0 {
		if replied, err := b.c.GetMessage(msg.ChatId, msg.ReplyToMessageID()); err == nil {
			if uid, ok := gotdbot.SenderUserID(replied.SenderId); ok && b.c.Me != nil && uid == b.c.Me.Id {
				triggered = true
			}
		}
	}
	if !triggered {
		return nil
	}

	prompt := text
	if username != "" {
		prompt = stripMention(text, username)
	}
	b.replyAI(c, msg, prompt)
	return nil
}

func (b *Bot) replyAI(c *gotdbot.Client, msg *gotdbot.Message, prompt string) {
	ctx := context.Background()
	stopTyping := b.startTyping(ctx, c, msg.ChatId)
	answer, err := b.ai.AskSession(ctx, ai.SessionKey(msg.ChatId), prompt)
	stopTyping()
	if err != nil {
		b.log.Warn("ai request failed", "err", err.Error())
		_, _ = msg.ReplyText(c, "AI request failed: "+err.Error(), nil)
		return
	}
	for _, chunk := range ai.SplitText(answer, 4000) {
		if _, err := c.SendTextMessage(msg.ChatId, chunk, &gotdbot.SendTextMessageOpts{
			ReplyToMessageID: msg.Id,
		}); err != nil {
			if gotdbot.IsFloodWait(err) {
				if secs := gotdbot.FloodWaitSeconds(err); secs > 0 && secs <= 120 {
					time.Sleep(time.Duration(secs) * time.Second)
					_, _ = c.SendTextMessage(msg.ChatId, chunk, &gotdbot.SendTextMessageOpts{ReplyToMessageID: msg.Id})
				}
			}
			break
		}
	}
}

// startTyping keeps the typing action alive until the returned stop func is
// called, covering slow model responses.
func (b *Bot) startTyping(ctx context.Context, c *gotdbot.Client, chatID int64) func() {
	ctx, cancel := context.WithCancel(ctx)
	send := func() {
		_ = c.SendChatAction("", chatID, &gotdbot.SendChatActionOpts{Action: gotdbot.ChatActionTyping{}})
	}
	go func() {
		send()
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				send()
			}
		}
	}()
	return cancel
}

// cmdReset clears the AI conversation memory for the current chat (owner only).
func (b *Bot) cmdReset(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	if b.ai == nil {
		_, err := msg.ReplyText(c, "Chatbot is not enabled.", nil)
		return err
	}
	b.ai.ResetSession(context.Background(), ai.SessionKey(msg.ChatId))
	_, err := msg.ReplyText(c, "Memory cleared. Fresh start!", nil)
	return err
}

func (b *Bot) botUsername() string {
	if b.c.Me == nil || b.c.Me.Usernames == nil {
		return ""
	}
	if len(b.c.Me.Usernames.ActiveUsernames) > 0 {
		return b.c.Me.Usernames.ActiveUsernames[0]
	}
	return ""
}

func stripMention(text, username string) string {
	re := "@" + username
	idx := strings.Index(strings.ToLower(text), strings.ToLower(re))
	if idx < 0 {
		return strings.TrimSpace(text)
	}
	out := text[:idx] + text[idx+len(re):]
	return strings.TrimSpace(out)
}
