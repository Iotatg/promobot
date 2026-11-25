package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Iotatg/gotdbot"

	"promobot/internal/ai"
	"promobot/internal/config"
	"promobot/internal/media"
	"promobot/internal/notify"
	"promobot/internal/scheduler"
	"promobot/internal/store"
)

// Assistant is the userbot side of Promobot. It is implemented by the gotd
// based userbot; the primary bot client is a *gotdbot.Client.
type Assistant interface {
	// ResolveChat resolves a target (id, @username or t.me link) to a chat id.
	ResolveChat(target string) (int64, error)
	// IsAdmin reports whether the assistant is an admin of the channel.
	IsAdmin(chatID int64) (bool, error)
	// SendText sends a text message as the assistant.
	SendText(chatID int64, text string) error
	// SendMedia sends a media attachment as the assistant.
	SendMedia(chatID int64, m store.Media) error
	// ForwardMessage forwards a message from fromChatID to chatID as the
	// assistant, preserving the "Forwarded from" header. It returns the id of
	// the new message (0 when unknown).
	ForwardMessage(chatID, fromChatID int64, msgID int, silent bool) (int, error)
	// EditCaption updates the caption/text of a message the assistant sent.
	EditCaption(chatID int64, msgID int, caption string) error
	// PinMessage pins a message in a chat as the assistant.
	PinMessage(chatID int64, msgID int, silent bool) error
	// KangStickerFile uploads raw sticker bytes and saves them into the
	// assistant's sticker pack, returning a short summary line.
	KangStickerFile(ctx context.Context, name string, data []byte, mime, emoji string) (string, error)
	// AutoStickerPacks returns display lines for the assistant's auto-save packs.
	AutoStickerPacks() []string
	// LeaveChat makes the assistant leave a single named chat.
	LeaveChat(chatID int64) error
	// Close stops the assistant.
	Close()
}

// SessionManager controls the assistant accounts at runtime.
type SessionManager interface {
	// AddSession connects using the given session string and persists it,
	// returning the account id (its Telegram user id) and user id.
	AddSession(ctx context.Context, session string) (id string, userID int64, err error)
	// RemoveSession disconnects and forgets one account, or all when id is "".
	RemoveSession(ctx context.Context, id string) error
	// AccountLines returns one display line per connected account.
	AccountLines() []string
	// Connected reports whether at least one account is connected.
	Connected() bool
	// SelfID returns the primary account's user id, or 0.
	SelfID() int64
	// SetSystem updates the AI system prompt on every connected account.
	SetSystem(system string)
	// SetPersonaName updates the group trigger/identity name on every account.
	SetPersonaName(name string)
}

// Bot is the owner-facing controller built on gotdbot.
type Bot struct {
	c         *gotdbot.Client
	cfg       *config.Config
	store     store.Store
	sched     *scheduler.Service
	log       *notify.Logger
	assistant Assistant
	ai        *ai.Client
	sessions  SessionManager

	// Mood sticker state (per-chat no-repeat cycling).
	stickerMu     sync.Mutex
	stickerCycles map[string][]string
	stickerLast   map[string]string
	stickerCache  map[string]string
}

// New constructs a Bot.
func New(c *gotdbot.Client, cfg *config.Config, st store.Store, sched *scheduler.Service, log *notify.Logger, assistant Assistant, aiClient *ai.Client) *Bot {
	return &Bot{c: c, cfg: cfg, store: st, sched: sched, log: log, assistant: assistant, ai: aiClient}
}

// SetSessionManager installs the runtime assistant session manager.
func (b *Bot) SetSessionManager(sm SessionManager) {
	b.sessions = sm
}

// Register wires all command and callback handlers.
func (b *Bot) Register() {
	// Track served users in an earlier group so commands/AI still run.
	b.c.OnMessageGroup(b.onTrackUser, func(m *gotdbot.Message) bool {
		return m != nil && !m.IsOutgoing && m.ChatId > 0
	}, -1)

	b.c.OnCommand("start", b.cmdStart)
	b.c.OnCommand("menu", b.cmdMenu)
	b.c.OnCommand("help", b.cmdHelp)
	b.c.OnCommand("id", b.cmdID)
	b.c.OnCommand("addchannel", b.cmdAddChannel)
	b.c.OnCommand("delchannel", b.cmdDelChannel)
	b.c.OnCommand("leave", b.cmdLeave)
	b.c.OnCommand("persona", b.cmdPersona)
	b.c.OnCommand("channels", b.cmdChannels)
	b.c.OnCommand("newpost", b.cmdNewPost)
	b.c.OnCommand("posts", b.cmdPosts)
	b.c.OnCommand("broadcast", b.cmdBroadcast)
	b.c.OnCommand("users", b.cmdUsers)
	b.c.OnCommand("addsticker", b.cmdAddSticker)
	b.c.OnCommand("addpack", b.cmdAddPack)
	b.c.OnCommand("stickerpacks", b.cmdStickerPacks)
	b.c.OnCommand("clearstickers", b.cmdClearStickers)
	b.c.OnCommand("sticker", b.cmdSticker)
	b.c.OnCommand("previewsticker", b.cmdPreviewSticker)
	b.c.OnCommand("kang", b.cmdKang)
	b.c.OnCommand("setstring", b.cmdSetString)
	b.c.OnCommand("delstring", b.cmdDelString)
	b.c.OnCommand("accounts", b.cmdAccounts)
	b.c.OnCallbackQuery(b.onCallback, nil)

	if b.ai != nil {
		b.c.OnCommand("ask", b.cmdAsk)
		b.c.OnCommand("chat", b.cmdChat)
		b.c.OnCommand("model", b.cmdModel)
		b.c.OnCommand("reset", b.cmdReset)
		b.c.OnMessage(b.onAutoReply, nil)
	}
}

func (b *Bot) isOwner(msg *gotdbot.Message) bool {
	id, ok := gotdbot.SenderUserID(msg.SenderId)
	return ok && id == b.cfg.OwnerID
}

// resolveAndCheck resolves the target chat for the given sender and verifies
// that the sending account is an admin of it.
func (b *Bot) resolveAndCheck(sender, target string) (int64, bool, error) {
	if sender == store.SenderAssistant {
		if b.assistant == nil {
			return 0, false, fmt.Errorf("assistant is not available")
		}
		chatID, err := b.assistant.ResolveChat(target)
		if err != nil {
			return 0, false, err
		}
		ok, err := b.assistant.IsAdmin(chatID)
		return chatID, ok, err
	}
	chatID, err := b.resolveChat(b.c, target)
	if err != nil {
		return 0, false, err
	}
	ok, err := b.isSenderAdmin(b.c, chatID)
	return chatID, ok, err
}

func (b *Bot) cmdStart(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		_, _ = msg.ReplyText(c, "This bot is private. You are not authorized.", nil)
		return nil
	}
	go b.bootAnimation(msg.ChatId)
	return nil
}

func (b *Bot) cmdMenu(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	_, err := c.SendTextMessage(msg.ChatId, b.menuText(), &gotdbot.SendTextMessageOpts{ReplyMarkup: b.mainMenu()})
	return err
}

func (b *Bot) cmdHelp(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	_, err := msg.ReplyText(c, helpText, nil)
	return err
}

func (b *Bot) cmdID(c *gotdbot.Client, msg *gotdbot.Message) error {
	id, _ := gotdbot.SenderUserID(msg.SenderId)
	_, err := msg.ReplyText(c, fmt.Sprintf("Your user ID: %d\nThis chat ID: %d", id, msg.ChatId), nil)
	return err
}

func (b *Bot) cmdAddChannel(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	args := strings.TrimSpace(msg.Args())
	if args == "" {
		_, err := msg.ReplyText(c, "Usage: /addchannel <chat_id|@username> [bot|assistant]", nil)
		return err
	}
	fields := strings.Fields(args)
	target := fields[0]
	sender := store.SenderBot
	if len(fields) > 1 && (fields[1] == store.SenderAssistant || fields[1] == store.SenderBot) {
		sender = fields[1]
	}

	chatID, ok, err := b.resolveAndCheck(sender, target)
	if err != nil {
		_, rerr := msg.ReplyText(c, "Could not resolve chat: "+err.Error(), nil)
		return rerr
	}
	if !ok {
		_, rerr := msg.ReplyText(c, fmt.Sprintf("The %s account is not an admin of that chat.", sender), nil)
		return rerr
	}

	title := ""
	if chat, cerr := c.GetChat(chatID); cerr == nil {
		title = chat.Title
	}
	ch := store.Channel{ChatID: chatID, Title: title, Sender: sender, AddedAt: time.Now().Unix()}
	if err := b.store.UpsertChannel(context.Background(), ch); err != nil {
		_, rerr := msg.ReplyText(c, "Save failed: "+err.Error(), nil)
		return rerr
	}
	b.log.Info("channel added", "chat", chatID, "title", title, "sender", sender)
	_, err = msg.ReplyText(c, fmt.Sprintf("Added: %s (%d) via %s.", title, chatID, sender), nil)
	return err
}

func (b *Bot) cmdChannels(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	return b.sendChannels(c, msg.ChatId)
}

func (b *Bot) cmdPosts(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	return b.sendPosts(c, msg.ChatId)
}

func (b *Bot) cmdNewPost(c *gotdbot.Client, msg *gotdbot.Message) error {
	if !b.isOwner(msg) {
		return nil
	}
	ask := func(prompt string) (*gotdbot.Message, error) {
		if _, err := msg.ReplyText(c, prompt, nil); err != nil {
			return nil, err
		}
		return c.AskFrom(msg.ChatId, b.cfg.OwnerID, &gotdbot.WaitMessageOpts{
			Timeout:            4 * time.Minute,
			CancellationFilter: isCancel,
		})
	}

	chMsg, err := ask("New scheduled post. Send /cancel to abort.\nStep 1/4: send the target chat id (see /channels).")
	if err != nil {
		_, _ = msg.ReplyText(c, "Cancelled or timed out.", nil)
		return nil
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(chMsg.GetText()), 10, 64)
	if err != nil {
		_, _ = msg.ReplyText(c, "Invalid chat id. Aborting.", nil)
		return nil
	}
	ch, err := b.store.GetChannel(context.Background(), chatID)
	if err != nil {
		_, _ = msg.ReplyText(c, "Chat not registered. Add it first with /addchannel.", nil)
		return nil
	}

	txtMsg, err := ask("Step 2/4: send the announcement text.")
	if err != nil {
		_, _ = msg.ReplyText(c, "Cancelled or timed out.", nil)
		return nil
	}
	text := strings.TrimSpace(txtMsg.GetText())
	if text == "" {
		_, _ = msg.ReplyText(c, "Empty text. Aborting.", nil)
		return nil
	}

	mediaMsg, err := ask("Step 3/4: send a photo/video/document/sticker to attach, or send 'skip'.")
	if err != nil {
		_, _ = msg.ReplyText(c, "Cancelled or timed out.", nil)
		return nil
	}
	var attach *store.Media
	if !strings.EqualFold(strings.TrimSpace(mediaMsg.GetText()), "skip") {
		attach, err = media.Capture(b.c, mediaMsg, b.cfg.MediaDir)
		if err != nil {
			_, _ = msg.ReplyText(c, "Could not read the attachment: "+err.Error(), nil)
			return nil
		}
		if attach == nil {
			_, _ = msg.ReplyText(c, "No supported media in that message; continuing without an attachment.", nil)
		}
	}

	intMsg, err := ask("Step 4/4: send the interval in minutes (e.g. 60).")
	if err != nil {
		_, _ = msg.ReplyText(c, "Cancelled or timed out.", nil)
		return nil
	}
	mins, err := strconv.Atoi(strings.TrimSpace(intMsg.GetText()))
	if err != nil || mins < 1 {
		_, _ = msg.ReplyText(c, "Invalid interval. Aborting.", nil)
		return nil
	}

	sch := store.Schedule{
		ID:              store.NewID(),
		Name:            ch.Title,
		ChatID:          chatID,
		Text:            text,
		Media:           attach,
		IntervalMinutes: mins,
		Enabled:         true,
		CreatedAt:       time.Now().Unix(),
	}
	if err := b.store.InsertSchedule(context.Background(), sch); err != nil {
		_, _ = msg.ReplyText(c, "Save failed: "+err.Error(), nil)
		return nil
	}
	_ = b.sched.Reload(context.Background())
	b.log.Info("schedule created", "id", sch.ID, "chat", chatID, "interval_minutes", mins)
	_, err = msg.ReplyText(c, fmt.Sprintf("Scheduled every %d minute(s) to %s.", mins, ch.Title), nil)
	return err
}

// bootAnimation edits a single message through a fake boot sequence, then
// replaces it with the main menu.
func (b *Bot) bootAnimation(chatID int64) {
	sent, err := b.c.SendTextMessage(chatID, bootFrame(0, "initializing"), nil)
	if err != nil {
		b.log.Error("boot: send failed", "err", err.Error())
		return
	}
	steps := []struct {
		pct   int
		label string
	}{
		{10, "loading modules"},
		{50, "syncing database"},
		{100, "system online"},
	}
	for _, st := range steps {
		time.Sleep(550 * time.Millisecond)
		_ = b.editText(chatID, sent.Id, bootFrame(st.pct, st.label), nil)
	}
	time.Sleep(400 * time.Millisecond)
	_ = b.editText(chatID, sent.Id, b.menuText(), b.mainMenu())
}

func (b *Bot) onCallback(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery) error {
	if u.SenderUserId != b.cfg.OwnerID {
		_ = c.AnswerCallbackQuery(0, u.Id, "Not authorized", "", nil)
		return nil
	}
	data := callbackData(u)
	_ = c.AnswerCallbackQuery(0, u.Id, "", "", nil)

	switch {
	case data == "menu:home":
		_ = b.editText(u.ChatId, u.MessageId, b.menuText(), b.mainMenu())
	case data == "menu:channels":
		b.renderChannels(u.ChatId, u.MessageId)
	case data == "menu:posts":
		b.renderPosts(u.ChatId, u.MessageId)
	case data == "menu:help":
		_ = b.editText(u.ChatId, u.MessageId, helpText, nil)
	case data == "menu:add":
		_ = b.editText(u.ChatId, u.MessageId, "Send: /addchannel <chat_id|@username> [bot|assistant]", nil)
	case strings.HasPrefix(data, "delchan:"):
		b.handleDeleteChannel(u.ChatId, u.MessageId, strings.TrimPrefix(data, "delchan:"))
	case strings.HasPrefix(data, "toggle:"):
		b.handleToggle(u.ChatId, u.MessageId, strings.TrimPrefix(data, "toggle:"))
	case strings.HasPrefix(data, "delpost:"):
		b.handleDeletePost(u.ChatId, u.MessageId, strings.TrimPrefix(data, "delpost:"))
	}
	return nil
}

func (b *Bot) handleDeleteChannel(chatID, msgID int64, rawID string) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return
	}
	removed, _ := b.store.DeleteSchedulesByChatID(context.Background(), id)
	_ = b.store.DeleteChannel(context.Background(), id)
	_ = b.sched.Reload(context.Background())
	b.log.Info("channel deleted", "chat", id, "schedules_removed", removed)
	b.renderChannels(chatID, msgID)
}

func (b *Bot) handleToggle(chatID, msgID int64, id string) {
	sch, err := b.store.GetSchedule(context.Background(), id)
	if err != nil {
		return
	}
	sch.Enabled = !sch.Enabled
	_ = b.store.UpdateSchedule(context.Background(), *sch)
	_ = b.sched.Reload(context.Background())
	b.log.Info("schedule toggled", "id", id, "enabled", sch.Enabled)
	b.renderPosts(chatID, msgID)
}

func (b *Bot) handleDeletePost(chatID, msgID int64, id string) {
	_ = b.store.DeleteSchedule(context.Background(), id)
	_ = b.sched.Reload(context.Background())
	b.log.Info("schedule deleted", "id", id)
	b.renderPosts(chatID, msgID)
}

func (b *Bot) sendChannels(c *gotdbot.Client, chatID int64) error {
	text, markup := b.channelsView()
	_, err := c.SendTextMessage(chatID, text, &gotdbot.SendTextMessageOpts{ReplyMarkup: markup})
	return err
}

func (b *Bot) renderChannels(chatID, msgID int64) {
	text, markup := b.channelsView()
	_ = b.editText(chatID, msgID, text, markup)
}

func (b *Bot) channelsView() (string, gotdbot.ReplyMarkup) {
	chans, err := b.store.ListChannels(context.Background())
	if err != nil || len(chans) == 0 {
		return "No channels yet. Use /addchannel <chat_id|@username> [bot|assistant].", nil
	}
	var sb strings.Builder
	sb.WriteString("Channels:\n")
	rows := make([][]gotdbot.InlineKeyboardButton, 0, len(chans))
	for _, ch := range chans {
		sb.WriteString(fmt.Sprintf("- %s (%d) via %s\n", ch.Title, ch.ChatID, ch.Sender))
		rows = append(rows, gotdbot.InlineRow(gotdbot.CallbackButton("Delete "+ch.Title, fmt.Sprintf("delchan:%d", ch.ChatID))))
	}
	return sb.String(), gotdbot.InlineKeyboard(rows...)
}

func (b *Bot) sendPosts(c *gotdbot.Client, chatID int64) error {
	text, markup := b.postsView()
	_, err := c.SendTextMessage(chatID, text, &gotdbot.SendTextMessageOpts{ReplyMarkup: markup})
	return err
}

func (b *Bot) renderPosts(chatID, msgID int64) {
	text, markup := b.postsView()
	_ = b.editText(chatID, msgID, text, markup)
}

func (b *Bot) postsView() (string, gotdbot.ReplyMarkup) {
	schedules, err := b.store.ListSchedules(context.Background())
	if err != nil || len(schedules) == 0 {
		return "No scheduled posts yet. Use /newpost.", nil
	}
	var sb strings.Builder
	sb.WriteString("Scheduled posts:\n")
	rows := make([][]gotdbot.InlineKeyboardButton, 0, len(schedules))
	for _, s := range schedules {
		state := "OFF"
		if s.Enabled {
			state = "ON"
		}
		sb.WriteString(fmt.Sprintf("- [%s] %s -> %d (every %d min)\n", state, s.Name, s.ChatID, s.IntervalMinutes))
		rows = append(rows, gotdbot.InlineRow(
			gotdbot.CallbackButton(fmt.Sprintf("%s %s", state, s.Name), "toggle:"+s.ID),
			gotdbot.CallbackButton("Delete", "delpost:"+s.ID),
		))
	}
	return sb.String(), gotdbot.InlineKeyboard(rows...)
}

func (b *Bot) resolveChat(client *gotdbot.Client, target string) (int64, error) {
	if id, err := strconv.ParseInt(target, 10, 64); err == nil {
		return id, nil
	}
	username := strings.TrimPrefix(target, "@")
	if username == "" {
		return 0, fmt.Errorf("empty username")
	}
	chat, err := client.SearchPublicChat(username)
	if err != nil {
		return 0, err
	}
	return chat.Id, nil
}

func (b *Bot) isSenderAdmin(client *gotdbot.Client, chatID int64) (bool, error) {
	if client == nil || client.Me == nil {
		return false, fmt.Errorf("client is not authorized")
	}
	member, err := client.GetChatMember(chatID, gotdbot.MessageSenderUser{UserId: client.Me.Id})
	if err != nil {
		return false, err
	}
	return isAdminStatus(member.Status), nil
}

func isAdminStatus(status gotdbot.ChatMemberStatus) bool {
	switch status.(type) {
	case gotdbot.ChatMemberStatusCreator, *gotdbot.ChatMemberStatusCreator:
		return true
	case gotdbot.ChatMemberStatusAdministrator, *gotdbot.ChatMemberStatusAdministrator:
		return true
	}
	return false
}

func (b *Bot) editText(chatID, msgID int64, text string, markup gotdbot.ReplyMarkup) error {
	ft, err := b.c.GetFormattedText(text, nil, "")
	if err != nil {
		return err
	}
	_, err = b.c.EditMessageText(chatID, &gotdbot.InputMessageText{Text: ft}, msgID, &gotdbot.EditMessageTextOpts{ReplyMarkup: markup})
	return err
}

func (b *Bot) mainMenu() gotdbot.ReplyMarkup {
	return gotdbot.InlineKeyboard(
		gotdbot.InlineRow(gotdbot.CallbackButton("Add Channel", "menu:add")),
		gotdbot.InlineRow(
			gotdbot.CallbackButton("My Channels", "menu:channels"),
			gotdbot.CallbackButton("My Posts", "menu:posts"),
		),
		gotdbot.InlineRow(gotdbot.CallbackButton("Help", "menu:help")),
	)
}

func (b *Bot) menuText() string {
	return "Promobot is online.\n\nManage scheduled announcements for channels you own."
}

func callbackData(u *gotdbot.UpdateNewCallbackQuery) string {
	switch p := u.Payload.(type) {
	case *gotdbot.CallbackQueryPayloadData:
		return string(p.Data)
	case gotdbot.CallbackQueryPayloadData:
		return string(p.Data)
	}
	return ""
}

func isCancel(m *gotdbot.Message) bool {
	return strings.TrimSpace(m.GetText()) == "/cancel"
}

func bootFrame(pct int, label string) string {
	filled := pct / 10
	if filled < 0 {
		filled = 0
	}
	if filled > 10 {
		filled = 10
	}
	bar := strings.Repeat("#", filled) + strings.Repeat("-", 10-filled)
	return fmt.Sprintf("[SYSTEM BOOT]\n[%s] %d%%\n> %s", bar, pct, label)
}

const helpText = "Promobot - owner commands:\n\n" +
	"/start - boot animation and main menu\n" +
	"/addchannel <chat_id|@username> [bot|assistant] - register a chat you admin\n" +
	"/delchannel <chat_id> - unregister a chat (stops posting there)\n" +
	"/leave <chat_id> - make the assistant leave ONE registered chat\n" +
	"/channels - list registered channels\n" +
	"/persona - show or set the assistant's custom persona\n" +
	"/newpost - create a scheduled announcement\n" +
	"/posts - list, toggle or delete scheduled posts\n" +
	"/broadcast [text] [-silent] [-pin] [-user] [-nobot] - send to channels and/or users (or reply to a post)\n" +
	"/users - count users the bot can DM with /broadcast -user\n" +
	"/addsticker <mood> - save replied sticker to a mood pack\n" +
	"/addpack [mood] - import the replied sticker's whole pack into a mood pack\n" +
	"/sticker <mood> - send a random sticker from a mood pack\n" +
	"/previewsticker <mood> - send one sticker from a mood pack\n" +
	"/stickerpacks - list mood packs\n" +
	"/clearstickers <mood> - delete a mood pack\n" +
	"/setstring <session> - connect an assistant account (max 5, message is deleted)\n" +
	"/accounts - list connected assistant accounts\n" +
	"/delstring [id] - disconnect one account, or all when no id is given\n" +
	"/ask <question> - ask the AI assistant (or reply to a message with /ask)\n" +
	"/chat on|off - toggle mention-based auto replies in this chat\n" +
	"/model [name] - show or change the active AI model\n" +
	"/id - show your user id and this chat id\n" +
	"/help - this message\n\n" +
	"Only channels where the sending account is an admin can be registered. " +
	"Broadcast targets your registered channels only. " +
	"Joining/leaving is always explicit and one chat at a time (no bulk). " +
	"Send /cancel during /newpost to abort."
