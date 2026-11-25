package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Iotatg/gotdbot"

	"promobot/internal/media"
	"promobot/internal/store"
)

// cmdBroadcast sends a message (text or a replied-to post) to every registered
// channel, and optionally to every served user when -user is given. Channels are
// limited to Promobot's allowlist (chats the sending account admins) and users
// to people who have interacted with the bot; it never messages arbitrary dialogs.
//
// When the owner replies to a media/text post, the post is forwarded (so the
// "Forwarded from" tag and the original caption are preserved, like IotaXMedia).
// If the owner also types text with the command, that text becomes the new
// caption, overriding the original.
//
// Flags: -silent, -pin / -pinloud, -user (also DM served users), -nobot (skip channels).
func (b *Bot) cmdBroadcast(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}

	full := msg.GetText()
	silent := strings.Contains(full, "-silent")
	pin := strings.Contains(full, "-pin") || strings.Contains(full, "-pinloud")
	toUsers := strings.Contains(full, "-user")
	noBot := strings.Contains(full, "-nobot")
	text := strings.TrimSpace(msg.Args())
	for _, flag := range []string{"-silent", "-pinloud", "-pin", "-user", "-nobot"} {
		text = strings.TrimSpace(strings.ReplaceAll(text, flag, ""))
	}

	var forward *gotdbot.Message
	if replyID := msg.ReplyToMessageID(); replyID != 0 {
		if replied, err := b.c.GetMessage(msg.ChatId, replyID); err == nil {
			forward = replied
		}
	}
	if forward == nil && text == "" {
		_, err := msg.ReplyText(c, "Usage: reply to a post with /broadcast [-silent] [-pin] [-user], or /broadcast <text> [-user]", nil)
		return err
	}
	if noBot && !toUsers {
		_, err := msg.ReplyText(c, "Nothing to do: -nobot set without -user.", nil)
		return err
	}

	chans, err := b.store.ListChannels(context.Background())
	if err != nil {
		_, rerr := msg.ReplyText(c, "Failed to load channels: "+err.Error(), nil)
		return rerr
	}
	var users []int64
	if toUsers {
		users, err = b.store.ListUsers(context.Background())
		if err != nil {
			_, rerr := msg.ReplyText(c, "Failed to load users: "+err.Error(), nil)
			return rerr
		}
	}
	if !toUsers && len(chans) == 0 {
		_, rerr := msg.ReplyText(c, "No channels registered. Use /addchannel first.", nil)
		return rerr
	}
	if noBot && len(users) == 0 {
		_, rerr := msg.ReplyText(c, "No served users yet.", nil)
		return rerr
	}

	status, _ := msg.ReplyText(c, fmt.Sprintf("Broadcasting to %d channel(s), %d user(s)...", len(chans), len(users)), nil)

	chatSent, chatFailed := 0, 0
	if !noBot {
		for _, ch := range chans {
			if err := b.deliver(ch, text, forward, silent, pin); err != nil {
				if gotdbot.IsFloodWait(err) {
					secs := gotdbot.FloodWaitSeconds(err)
					if secs > 0 && secs <= 200 {
						time.Sleep(time.Duration(secs) * time.Second)
						if err2 := b.deliver(ch, text, forward, silent, pin); err2 == nil {
							chatSent++
						} else {
							chatFailed++
						}
						continue
					}
				}
				chatFailed++
				b.log.Warn("broadcast: send failed", "chat", ch.ChatID, "err", err.Error())
				continue
			}
			chatSent++
			time.Sleep(400 * time.Millisecond)
		}
	}

	userSent, userFailed := 0, 0
	for _, uid := range users {
		if err := b.deliverUser(uid, text, forward, silent); err != nil {
			if gotdbot.IsFloodWait(err) {
				secs := gotdbot.FloodWaitSeconds(err)
				if secs > 0 && secs <= 200 {
					time.Sleep(time.Duration(secs) * time.Second)
					if err2 := b.deliverUser(uid, text, forward, silent); err2 == nil {
						userSent++
						continue
					}
				}
			}
			userFailed++
			b.log.Warn("broadcast: dm failed", "user", uid, "err", err.Error())
			continue
		}
		userSent++
		time.Sleep(400 * time.Millisecond)
	}

	b.log.Info("broadcast finished", "channels_sent", chatSent, "channels_failed", chatFailed, "users_sent", userSent, "users_failed", userFailed)
	if status != nil {
		summary := fmt.Sprintf("Broadcast done: %d/%d channels, %d/%d users sent.", chatSent, len(chans), userSent, len(users))
		_, _ = b.c.EditMessageText(msg.ChatId, mustFormatted(b.c, summary), status.Id, nil)
	}
	return nil
}

// deliverUser sends the post to a single served user as a private message. The
// bot forwards the replied post (preserving the "Forwarded from" header) or
// sends the text directly.
func (b *Bot) deliverUser(userID int64, text string, forward *gotdbot.Message, silent bool) error {
	if forward != nil {
		res, err := b.c.ForwardMessages(userID, forward.ChatId, []int64{forward.Id}, &gotdbot.ForwardMessagesOpts{
			SendCopy: false,
			Options:  &gotdbot.MessageSendOptions{DisableNotification: silent},
		})
		if err != nil {
			return err
		}
		id := firstMessageID(res)
		if id != 0 && strings.TrimSpace(text) != "" {
			_, _ = b.c.EditMessageCaption(userID, id, &gotdbot.EditMessageCaptionOpts{
				Caption: &gotdbot.FormattedText{Text: text},
			})
		}
		return nil
	}
	_, err := b.c.SendTextMessage(userID, text, &gotdbot.SendTextMessageOpts{DisableNotification: silent})
	return err
}

func (b *Bot) deliver(ch store.Channel, text string, forward *gotdbot.Message, silent, pin bool) error {
	if ch.Sender == store.SenderAssistant {
		if b.assistant == nil {
			return fmt.Errorf("assistant unavailable")
		}
		if forward == nil {
			return b.assistant.SendText(ch.ChatID, text)
		}

		// Preferred: when the replied post is itself a forward, forward straight
		// from the original channel. This is the common case (the owner forwards
		// a channel post into the bot chat) and the assistant is a member of that
		// channel, so the "Forwarded from" header is preserved exactly.
		if srcChat, srcMsg, ok := originChannel(forward); ok {
			if id, err := b.assistant.ForwardMessage(ch.ChatID, srcChat, srcMsg, silent); err == nil {
				b.finishAssistant(ch.ChatID, id, text, pin, silent)
				return nil
			} else {
				b.log.Warn("broadcast: origin forward failed", "chat", ch.ChatID, "src", srcChat, "err", err.Error())
			}
		}

		// Direct forward from the source chat; works when the assistant shares
		// that chat (e.g. the command was issued in a group it is in).
		if id, err := b.assistant.ForwardMessage(ch.ChatID, forward.ChatId, int(forward.Id), silent); err == nil {
			b.finishAssistant(ch.ChatID, id, text, pin, silent)
			return nil
		} else {
			b.log.Warn("broadcast: direct forward failed", "chat", ch.ChatID, "src", forward.ChatId, "err", err.Error())
		}

		// The assistant cannot see the bot's own DM, so relay the post through
		// the assistant's chat: the bot forwards it to the assistant, then the
		// assistant forwards it on. The original author survives the chain.
		if id, ok := b.relayForward(ch.ChatID, forward, silent); ok {
			b.finishAssistant(ch.ChatID, id, text, pin, silent)
			return nil
		}
		b.log.Warn("broadcast: forward unavailable, re-uploading", "chat", ch.ChatID)

		// Last resort: re-upload the attachment (loses the forward header).
		m, err := media.Capture(b.c, forward, b.cfg.MediaDir)
		if err != nil {
			return fmt.Errorf("capture attachment: %w", err)
		}
		if m != nil {
			mm := *m
			// The owner's typed text wins as the caption; otherwise keep the
			// original caption carried by the captured media.
			if strings.TrimSpace(text) != "" {
				mm.Caption = text
			}
			return b.assistant.SendMedia(ch.ChatID, mm)
		}
		body := text
		if body == "" {
			body = forward.GetText()
		}
		return b.assistant.SendText(ch.ChatID, body)
	}

	if forward != nil {
		res, err := b.c.ForwardMessages(ch.ChatID, forward.ChatId, []int64{forward.Id}, &gotdbot.ForwardMessagesOpts{
			SendCopy: false, // a real forward keeps the "Forwarded from" tag
			Options:  &gotdbot.MessageSendOptions{DisableNotification: silent},
		})
		if err != nil {
			return err
		}
		id := firstMessageID(res)
		if id != 0 {
			if strings.TrimSpace(text) != "" {
				_, _ = b.c.EditMessageCaption(ch.ChatID, id, &gotdbot.EditMessageCaptionOpts{
					Caption: &gotdbot.FormattedText{Text: text},
				})
			}
			if pin {
				_ = b.c.PinChatMessage(ch.ChatID, id, &gotdbot.PinChatMessageOpts{DisableNotification: silent})
			}
		}
		return nil
	}

	sent, err := b.c.SendTextMessage(ch.ChatID, text, &gotdbot.SendTextMessageOpts{DisableNotification: silent})
	if err != nil {
		return err
	}
	if pin && sent != nil {
		_ = b.c.PinChatMessage(ch.ChatID, sent.Id, &gotdbot.PinChatMessageOpts{DisableNotification: silent})
	}
	return nil
}

// finishAssistant applies the caption override and pin to a message the
// assistant just sent.
func (b *Bot) finishAssistant(chatID int64, id int, text string, pin, silent bool) {
	if id == 0 {
		return
	}
	if strings.TrimSpace(text) != "" {
		_ = b.assistant.EditCaption(chatID, id, text)
	}
	if pin {
		_ = b.assistant.PinMessage(chatID, id, silent)
	}
}

// relayForward forwards the post to the assistant via the bot, then has the
// assistant forward it on to the target channel. This lets the assistant post a
// real forward even when it cannot access the source chat (e.g. the bot's DM).
// The temporary relay message is deleted afterwards.
//
// Note: this requires the assistant account to have started the bot (so the bot
// may DM it).
func (b *Bot) relayForward(chatID int64, forward *gotdbot.Message, silent bool) (int, bool) {
	if b.sessions == nil || forward == nil {
		return 0, false
	}
	assistantID := b.sessions.SelfID()
	if assistantID == 0 {
		return 0, false
	}
	me, err := b.c.GetMe()
	if err != nil || me == nil || me.Id == 0 {
		return 0, false
	}
	botID := me.Id
	// The bot forwards the post into the assistant's DM with the bot.
	res, err := b.c.ForwardMessages(assistantID, forward.ChatId, []int64{forward.Id}, &gotdbot.ForwardMessagesOpts{SendCopy: false})
	if err != nil {
		b.log.Warn("broadcast: relay handoff failed", "err", err.Error())
		return 0, false
	}
	relayID := firstMessageID(res)
	if relayID == 0 {
		return 0, false
	}
	defer func() {
		_ = b.c.DeleteMessages(assistantID, []int64{relayID}, &gotdbot.DeleteMessagesOpts{Revoke: true})
	}()
	// The assistant forwards that message on; the source chat is its DM with the
	// bot (peer id = the bot's user id).
	id, err := b.assistant.ForwardMessage(chatID, botID, int(relayID), silent)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

// originChannel returns the original channel chat id and message id when the
// given message is a forward that originated in a channel.
func originChannel(m *gotdbot.Message) (int64, int, bool) {
	if m == nil || m.ForwardInfo == nil {
		return 0, 0, false
	}
	oc, ok := m.ForwardInfo.Origin.(*gotdbot.MessageOriginChannel)
	if !ok || oc.ChatId == 0 || oc.MessageId == 0 {
		return 0, 0, false
	}
	return oc.ChatId, int(oc.MessageId), true
}

// firstMessageID returns the id of the first message in a forwarded batch.
func firstMessageID(res *gotdbot.Messages) int64 {
	if res == nil || len(res.Messages) == 0 {
		return 0
	}
	return res.Messages[0].Id
}

func mustFormatted(c *gotdbot.Client, text string) gotdbot.InputMessageContent {
	ft, err := c.GetFormattedText(text, nil, "")
	if err != nil {
		return &gotdbot.InputMessageText{Text: &gotdbot.FormattedText{Text: text}}
	}
	return &gotdbot.InputMessageText{Text: ft}
}
