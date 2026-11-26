// Package assistant implements the userbot side of Promobot using a pure-Go
// MTProto client (gotd). It imports an existing Pyrogram StringSession so the
// user can post to channels from their own account without an interactive
// TDLib login.
package assistant

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"

	"promobot/internal/ai"
	"promobot/internal/media"
	"promobot/internal/store"
)

// channelPrefix is added to raw channel IDs to form the bot-style chat ID
// used across Promobot (e.g. -1001234567890).
const channelPrefix int64 = 1000000000000

// cooldown is the minimum interval between replies to the same user.
const cooldown = 2 * time.Second

// Client is a connected MTProto userbot.
type Client struct {
	api    *tg.Client
	pm     *peers.Manager
	ai     *ai.Client
	selfID int64

	// Trigger configuration for group replies.
	personaName  string
	personaRe    *regexp.Regexp
	selfUsername string

	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	lastReply map[int64]time.Time

	reactMu   sync.Mutex
	lastReact map[int64]time.Time
	reactMode string

	packsMu   sync.Mutex
	autoPacks map[string]*PackInfo
	// autoSavedPacks caches the derived mood for a learned sticker set key,
	// mirroring IotaXMedia's _AUTO_SAVED_PACKS.
	autoSavedPacks map[string]string

	// st, when set, gives the assistant access to the shared mood sticker packs
	// so it can learn and reply sticker-to-sticker (IotaXMedia parity).
	st store.Store

	// moodSticker, when set, returns a local sticker file for the given mood
	// (the bot provides this from its /addsticker mood packs).
	moodSticker MoodStickerProvider

	// mediaSlots limits how many media analyses run concurrently.
	mediaSlots chan struct{}
	// albumMu/albumSeen de-duplicate album (media group) photos.
	albumMu   sync.Mutex
	albumSeen map[string]time.Time
}

// MoodStickerProvider returns a local path to a sticker for the given chat and
// mood, or ok=false when none is available. It is implemented by the bot.
type MoodStickerProvider func(chatID int64, mood string) (path string, ok bool)

// SetMoodStickerProvider installs the mood sticker provider.
func (c *Client) SetMoodStickerProvider(p MoodStickerProvider) {
	c.moodSticker = p
}

// SetStore gives the assistant access to the shared mood sticker packs so it can
// learn incoming sticker packs and reply sticker-to-sticker.
func (c *Client) SetStore(st store.Store) {
	c.st = st
}

// FromPyrogramSession decodes a Pyrogram StringSession and connects using its
// authorization key. The session string uses the big-endian layout
// ">BI?256sQ?" (dc, api_id, test_mode, auth_key, user_id, is_bot). When ai is
// non-nil the assistant also replies to direct messages.
func FromPyrogramSession(appID int, appHash, sessionString string, aiClient *ai.Client) (*Client, error) {
	dc, authKey, userID, isBot, err := decodePyrogramSession(sessionString)
	if err != nil {
		return nil, err
	}

	mem := &session.StorageMemory{}
	loader := &session.Loader{Storage: mem}
	data := &session.Data{
		DC:        dc,
		AuthKey:   authKey,
		AuthKeyID: authKeyID(authKey),
		UserID:    userID,
		Bot:       isBot,
	}
	if err := loader.Save(context.Background(), data); err != nil {
		return nil, fmt.Errorf("seed session: %w", err)
	}

	c := &Client{done: make(chan struct{}), ai: aiClient, lastReply: make(map[int64]time.Time),
		lastReact: make(map[int64]time.Time), reactMode: ReactModeBoth,
		mediaSlots: make(chan struct{}, maxInFlight), albumSeen: make(map[string]time.Time)}
	raw := telegram.NewClient(appID, appHash, telegram.Options{
		SessionStorage: mem,
		UpdateHandler:  telegram.UpdateHandlerFunc(c.handleUpdate),
	})

	runCtx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	ready := make(chan error, 1)
	go func() {
		err := raw.Run(runCtx, func(ctx context.Context) error {
			c.api = raw.API()
			c.pm = peers.Options{}.Build(raw.API())
			if err := c.pm.Init(ctx); err != nil {
				return fmt.Errorf("init peers: %w", err)
			}
			self, err := c.pm.Self(ctx)
			if err != nil {
				return fmt.Errorf("resolve self: %w", err)
			}
			c.selfID = self.ID()
			if uname, ok := self.Username(); ok {
				c.selfUsername = strings.ToLower(uname)
			}
			select {
			case ready <- nil:
			default:
			}
			go c.WarmAutoPacks(ctx)
			<-ctx.Done()
			return nil
		})
		if runCtx.Err() == nil {
			select {
			case ready <- err:
			default:
			}
		}
		close(c.done)
	}()

	select {
	case err := <-ready:
		if err != nil {
			cancel()
			<-c.done
			return nil, err
		}
	case <-time.After(60 * time.Second):
		cancel()
		<-c.done
		return nil, fmt.Errorf("timeout connecting assistant")
	}
	return c, nil
}

// SelfID returns the assistant user's Telegram ID.
func (c *Client) SelfID() int64 { return c.selfID }

// Close stops the client.
func (c *Client) Close() {
	if c.cancel != nil {
		c.cancel()
	}
	if c.done != nil {
		<-c.done
	}
}

// ResolveChat resolves a target (numeric chat id, @username or t.me link) to a
// bot-style chat ID.
func (c *Client) ResolveChat(target string) (int64, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return 0, fmt.Errorf("empty target")
	}
	if id, err := strconv.ParseInt(target, 10, 64); err == nil {
		return id, nil
	}
	p, err := c.pm.Resolve(context.Background(), target)
	if err != nil {
		return 0, err
	}
	return botID(p), nil
}

// IsAdmin reports whether the assistant is an admin of the channel.
func (c *Client) IsAdmin(chatID int64) (bool, error) {
	if chatID > -channelPrefix {
		return false, fmt.Errorf("not a channel id")
	}
	ctx := context.Background()
	ch, err := c.pm.ResolveChannelID(ctx, rawChannelID(chatID))
	if err != nil {
		return false, err
	}
	ip, ok := ch.InputPeer().(*tg.InputPeerChannel)
	if !ok {
		return false, fmt.Errorf("unexpected peer type")
	}
	res, err := c.api.ChannelsGetParticipant(ctx, &tg.ChannelsGetParticipantRequest{
		Channel:     &tg.InputChannel{ChannelID: ip.ChannelID, AccessHash: ip.AccessHash},
		Participant: &tg.InputPeerSelf{},
	})
	if err != nil {
		return false, err
	}
	switch res.Participant.(type) {
	case *tg.ChannelParticipantCreator, *tg.ChannelParticipantAdmin:
		return true, nil
	}
	return false, nil
}

// CanPost reports whether the assistant can post in the given chat. Admins can
// always post; a plain member may post in supergroups and basic groups when
// sending is not restricted. Broadcast channels still require admin rights.
func (c *Client) CanPost(chatID int64) (bool, error) {
	ctx := context.Background()
	switch {
	case chatID <= -channelPrefix:
		ch, err := c.pm.ResolveChannelID(ctx, rawChannelID(chatID))
		if err != nil {
			return false, err
		}
		raw := ch.Raw()
		ip, ok := ch.InputPeer().(*tg.InputPeerChannel)
		if !ok {
			return false, fmt.Errorf("unexpected peer type")
		}
		res, err := c.api.ChannelsGetParticipant(ctx, &tg.ChannelsGetParticipantRequest{
			Channel:     &tg.InputChannel{ChannelID: ip.ChannelID, AccessHash: ip.AccessHash},
			Participant: &tg.InputPeerSelf{},
		})
		if err != nil {
			return false, err
		}
		switch res.Participant.(type) {
		case *tg.ChannelParticipantCreator, *tg.ChannelParticipantAdmin:
			return true, nil
		case *tg.ChannelParticipant, *tg.ChannelParticipantSelf:
			// A regular member (ChannelParticipantSelf is returned when the
			// participant is the requesting account itself).
			if !raw.Megagroup {
				return false, fmt.Errorf("assistant is only a member; admin is required for broadcast channels")
			}
			if raw.DefaultBannedRights.SendMessages {
				return false, fmt.Errorf("members cannot send messages in this supergroup")
			}
			return true, nil
		case *tg.ChannelParticipantBanned:
			return false, fmt.Errorf("assistant is restricted in this chat")
		}
		return false, fmt.Errorf("assistant is not a member of this chat")
	case chatID < 0:
		chat, err := c.pm.ResolveChatID(ctx, -chatID)
		if err != nil {
			return false, err
		}
		raw := chat.Raw()
		if raw.Left {
			return false, fmt.Errorf("assistant has left this group")
		}
		if raw.DefaultBannedRights.SendMessages {
			return false, fmt.Errorf("members cannot send messages in this group")
		}
		return true, nil
	default:
		return false, fmt.Errorf("not a group or channel")
	}
}

// LeaveChat makes the assistant leave a single channel/supergroup it is a
// member of. Callers must scope this to one explicitly named, owner-approved
// chat; it is never used for bulk leaving.
func (c *Client) LeaveChat(chatID int64) error {
	peer, err := c.inputPeer(chatID)
	if err != nil {
		return err
	}
	ip, ok := peer.(*tg.InputPeerChannel)
	if !ok {
		return fmt.Errorf("not a channel/supergroup")
	}
	_, err = c.api.ChannelsLeaveChannel(context.Background(), &tg.InputChannel{
		ChannelID:  ip.ChannelID,
		AccessHash: ip.AccessHash,
	})
	return err
}

// SendText sends a text message as the assistant.
func (c *Client) SendText(chatID int64, text string) error {
	peer, err := c.inputPeer(chatID)
	if err != nil {
		return err
	}
	sender := message.NewSender(c.api)
	_, err = sender.To(peer).Text(context.Background(), text)
	return err
}

// SendMedia uploads and sends a media attachment as the assistant.
func (c *Client) SendMedia(chatID int64, m store.Media) error {
	return c.sendMedia(chatID, m, 0)
}

// sendMedia uploads and sends a media attachment, optionally quoting the
// message identified by replyTo (0 means no reply).
func (c *Client) sendMedia(chatID int64, m store.Media, replyTo int) error {
	if m.IsRemote {
		return fmt.Errorf("assistant cannot send remote media file ids")
	}
	peer, err := c.inputPeer(chatID)
	if err != nil {
		return err
	}
	ctx := context.Background()
	s := message.NewSender(c.api).To(peer)
	b := &s.Builder
	if replyTo > 0 {
		b = b.Reply(replyTo)
	}
	if m.Kind == media.KindSticker {
		c.chooseSticker(ctx, peer)
		_, err := b.Upload(message.FromPath(m.Source)).Sticker(ctx)
		return err
	}
	up := b.Upload(message.FromPath(m.Source))
	caption := styling.Plain(m.Caption)
	switch m.Kind {
	case media.KindPhoto:
		_, err = up.Photo(ctx, caption)
	case media.KindVideo:
		_, err = up.Video(ctx, caption)
	case media.KindAudio:
		_, err = up.Audio(ctx, caption)
	case media.KindAnimation:
		_, err = up.GIF(ctx, caption)
	default:
		_, err = up.File(ctx, caption)
	}
	return err
}

// chooseSticker sends the "choosing a sticker" chat action, best-effort.
func (c *Client) chooseSticker(ctx context.Context, peer tg.InputPeerClass) {
	_ = message.NewSender(c.api).To(peer).TypingAction().ChooseSticker(ctx)
}

func (c *Client) inputPeer(chatID int64) (tg.InputPeerClass, error) {
	ctx := context.Background()
	switch {
	case chatID <= -channelPrefix:
		ch, err := c.pm.ResolveChannelID(ctx, rawChannelID(chatID))
		if err != nil {
			return nil, err
		}
		return ch.InputPeer(), nil
	case chatID < 0:
		ch, err := c.pm.ResolveChatID(ctx, -chatID)
		if err != nil {
			return nil, err
		}
		return ch.InputPeer(), nil
	default:
		u, err := c.pm.ResolveUserID(ctx, chatID)
		if err != nil {
			return nil, err
		}
		return u.InputPeer(), nil
	}
}

// botID converts a gotd peer into the bot-style chat ID used by Promobot.
func botID(p peers.Peer) int64 {
	id := p.ID()
	t := p.TDLibPeerID()
	switch {
	case t.IsChannel():
		return -channelPrefix - id
	case t.IsChat():
		return -id
	default:
		return id
	}
}

// rawChannelID converts a bot-style channel chat ID back to a raw channel ID.
func rawChannelID(chatID int64) int64 {
	return -chatID - channelPrefix
}

// decodePyrogramSession parses a Pyrogram StringSession.
func decodePyrogramSession(s string) (dc int, authKey []byte, userID int64, isBot bool, err error) {
	s = strings.TrimSpace(s)
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	raw, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		err = fmt.Errorf("decode session: %w", err)
		return
	}
	if len(raw) < 271 {
		err = fmt.Errorf("session too short: %d bytes", len(raw))
		return
	}
	dc = int(raw[0])
	authKey = raw[6:262]
	if len(authKey) != 256 {
		err = fmt.Errorf("invalid auth key length: %d", len(authKey))
		return
	}
	userID = int64(binary.BigEndian.Uint64(raw[262:270]))
	isBot = raw[270] != 0
	return
}

func authKeyID(key []byte) []byte {
	h := sha1.Sum(key)
	return h[12:20]
}

// handleUpdate is the gotd update handler. It only reacts to direct messages
// (private chats) sent to the assistant.
func (c *Client) handleUpdate(ctx context.Context, u tg.UpdatesClass) error {
	m := extractMessage(u)
	if m == nil || c.pm == nil || c.api == nil {
		return nil
	}
	c.applyEntities(ctx, u)
	slog.Debug("assistant update", "peer", peerKind(m.PeerID), "media", mediaKindName(m), "out", m.Out)
	go c.processMessage(context.Background(), m)
	return nil
}

// applyEntities caches the users/chats carried by an update so the peers manager
// can resolve senders (needed to mention users in group replies).
func (c *Client) applyEntities(ctx context.Context, u tg.UpdatesClass) {
	switch u := u.(type) {
	case *tg.Updates:
		_ = c.pm.Apply(ctx, u.Users, u.Chats)
	case *tg.UpdatesCombined:
		_ = c.pm.Apply(ctx, u.Users, u.Chats)
	}
}

func peerKind(p tg.PeerClass) string {
	switch p.(type) {
	case *tg.PeerUser:
		return "user"
	case *tg.PeerChat:
		return "chat"
	case *tg.PeerChannel:
		return "channel"
	default:
		return "unknown"
	}
}

func mediaKindName(m *tg.Message) string {
	switch md := m.Media.(type) {
	case *tg.MessageMediaPhoto:
		return "photo"
	case *tg.MessageMediaDocument:
		doc, _ := md.Document.(*tg.Document)
		if doc == nil {
			return "document?"
		}
		return doc.MimeType
	default:
		return "none"
	}
}

func extractMessage(u tg.UpdatesClass) *tg.Message {
	var updates []tg.UpdateClass
	switch u := u.(type) {
	case *tg.Updates:
		updates = u.Updates
	case *tg.UpdatesCombined:
		updates = u.Updates
	case *tg.UpdateShort:
		updates = []tg.UpdateClass{u.Update}
	default:
		return nil
	}
	for _, upd := range updates {
		switch upd := upd.(type) {
		case *tg.UpdateNewMessage:
			if m, ok := upd.Message.(*tg.Message); ok {
				return m
			}
		case *tg.UpdateNewChannelMessage:
			if m, ok := upd.Message.(*tg.Message); ok {
				return m
			}
		}
	}
	return nil
}

func (c *Client) processMessage(ctx context.Context, m *tg.Message) {
	if m == nil || m.Out {
		return
	}
	if c.selfID != 0 {
		if u, ok := m.FromID.(*tg.PeerUser); ok && u.UserID == c.selfID {
			return
		}
	}

	// Drop a contextual emoji reaction on incoming messages (IotaXMedia parity),
	// independent of whether the assistant also replies.
	c.maybeReact(ctx, m)

	isDM := false
	if _, ok := m.PeerID.(*tg.PeerUser); ok {
		isDM = true
	}

	triggered := isDM
	if !triggered {
		triggered = c.mentionsOrReplies(ctx, m, m.Message)
	}
	if !triggered {
		slog.Debug("assistant message ignored (group, not addressed)",
			"chat", chatIDFromPeer(m.PeerID), "sender", senderForLog(m),
			"sticker", isSticker(m), "reply", m.ReplyTo != nil, "text", truncate(m.Message))
		return
	}
	if c.fromBot(ctx, m) {
		return
	}
	slog.Info("assistant handling message", "dm", isDM, "chat", chatIDFromPeer(m.PeerID), "sender", senderForLog(m), "media", mediaKindName(m), "text", truncate(m.Message))

	if isSticker(m) {
		if !c.allow(m) {
			return
		}
		c.replyMoodSticker(ctx, m)
		return
	}
	if kind, ok := messageMediaKind(m); ok {
		if c.ai == nil || !c.allow(m) {
			slog.Debug("media branch skipped", "kind", kind, "ai", c.ai != nil)
			return
		}
		c.replyMedia(ctx, m, kind)
		return
	}
	text := strings.TrimSpace(m.Message)
	if text == "" || strings.HasPrefix(text, "/") {
		return
	}
	if c.ai == nil || !c.allow(m) {
		return
	}
	if u, ok := m.FromID.(*tg.PeerUser); ok && c.isBot(ctx, u.UserID) {
		return
	}
	if !isDM {
		text = c.stripTrigger(text)
		if text == "" {
			// The assistant was addressed by name only (e.g. "Radhika"): greet
			// instead of staying silent.
			text = "hi"
		}
	}
	if answer, handled := c.tryReplyToMedia(ctx, m, text); handled {
		if answer != "" {
			c.ai.RememberTurn(ctx, ai.SessionKey(chatIDFromPeer(m.PeerID)), "[media reply] "+text, answer)
			c.sendToChat(ctx, m, answer)
		}
		return
	}
	stopTyping := c.startTyping(ctx, m.PeerID)
	answer, err := c.ai.AskSession(ctx, ai.SessionKey(chatIDFromPeer(m.PeerID)), text)
	stopTyping()
	if err != nil {
		return
	}
	c.sendToChat(ctx, m, answer)
}

// typing sends a "typing" chat action to the peer, best-effort.
func (c *Client) typing(ctx context.Context, peerID tg.PeerClass) {
	peer, err := c.inputPeer(chatIDFromPeer(peerID))
	if err != nil {
		return
	}
	_ = message.NewSender(c.api).To(peer).TypingAction().Typing(ctx)
}

// startTyping keeps sending the typing action every few seconds until the
// returned stop function is called, so the indicator stays alive during slow
// model calls.
func (c *Client) startTyping(ctx context.Context, peerID tg.PeerClass) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		c.typing(ctx, peerID)
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.typing(ctx, peerID)
			}
		}
	}()
	return cancel
}

// SetPersona sets the name used to trigger the assistant in groups (e.g.
// "Radhika"). It is matched case-insensitively as a whole word.
func (c *Client) SetPersona(name string) {
	c.personaName = strings.ToLower(strings.TrimSpace(name))
	if c.personaName != "" {
		c.personaRe = regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(c.personaName) + `\b`)
	} else {
		c.personaRe = nil
	}
}

// mentionsOrReplies reports whether a group message addresses the assistant by
// reply, @username, persona name, or a mention-name entity.
func (c *Client) mentionsOrReplies(ctx context.Context, m *tg.Message, text string) bool {
	if c.repliesToSelf(ctx, m) {
		return true
	}
	lower := strings.ToLower(text)
	if c.selfUsername != "" && strings.Contains(lower, "@"+c.selfUsername) {
		return true
	}
	for _, ent := range m.Entities {
		if mn, ok := ent.(*tg.MessageEntityMentionName); ok && c.selfID != 0 && mn.UserID == c.selfID {
			return true
		}
	}
	if c.personaRe != nil && c.personaRe.MatchString(text) {
		return true
	}
	return false
}

// repliesToSelf reports whether m is a reply to a message sent by this
// account. Telegram only populates the reply header's ReplyFrom for some
// cases (cross-chat/story replies), so when it is absent we fall back to
// fetching the replied-to message and checking its sender.
func (c *Client) repliesToSelf(ctx context.Context, m *tg.Message) bool {
	rh, ok := m.ReplyTo.(*tg.MessageReplyHeader)
	if !ok {
		return false
	}
	if fwd, ok := rh.GetReplyFrom(); ok {
		if u, ok := fwd.FromID.(*tg.PeerUser); ok && c.selfID != 0 && u.UserID == c.selfID {
			return true
		}
	}
	if c.selfID == 0 {
		return false
	}
	id, ok := rh.GetReplyToMsgID()
	if !ok || id == 0 {
		return false
	}
	peer := m.PeerID
	if p, ok := rh.GetReplyToPeerID(); ok && p != nil {
		peer = p
	}
	replied, err := c.fetchMessage(ctx, peer, id)
	if err != nil || replied == nil {
		return false
	}
	if replied.Out {
		return true
	}
	if u, ok := replied.FromID.(*tg.PeerUser); ok && u.UserID == c.selfID {
		return true
	}
	return false
}

// stripTrigger removes the @username and persona name from a group message so
// the AI sees only the user's actual prompt.
func (c *Client) stripTrigger(text string) string {
	if c.selfUsername != "" {
		re := regexp.MustCompile(`(?i)@` + regexp.QuoteMeta(c.selfUsername))
		text = re.ReplaceAllString(text, " ")
	}
	if c.personaRe != nil {
		text = c.personaRe.ReplaceAllString(text, " ")
	}
	return strings.TrimSpace(text)
}

// allow enforces a per-user cooldown.
func (c *Client) allow(m *tg.Message) bool {
	uid, ok := c.senderID(m)
	if !ok {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if last, ok := c.lastReply[uid]; ok && now.Sub(last) < cooldown {
		return false
	}
	c.lastReply[uid] = now
	return true
}

// senderID returns the message sender's user id. In private chats the peer
// itself is the sender, which covers history messages where FromID may be nil.
func (c *Client) senderID(m *tg.Message) (int64, bool) {
	if u, ok := m.FromID.(*tg.PeerUser); ok && u.UserID != 0 {
		return u.UserID, true
	}
	if pu, ok := m.PeerID.(*tg.PeerUser); ok && pu.UserID != 0 {
		return pu.UserID, true
	}
	return 0, false
}

// senderForLog returns the sender user id for logging, or 0 when unknown.
func senderForLog(m *tg.Message) int64 {
	if u, ok := m.FromID.(*tg.PeerUser); ok {
		return u.UserID
	}
	if pu, ok := m.PeerID.(*tg.PeerUser); ok {
		return pu.UserID
	}
	return 0
}

func (c *Client) isBot(ctx context.Context, userID int64) bool {
	u, err := c.pm.ResolveUserID(ctx, userID)
	if err != nil {
		return false
	}
	return u.Raw().GetBot()
}

// fromBot reports whether the message was sent by a bot account.
func (c *Client) fromBot(ctx context.Context, m *tg.Message) bool {
	u, ok := m.FromID.(*tg.PeerUser)
	if !ok {
		return false
	}
	return c.isBot(ctx, u.UserID)
}

func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
}

// sendToChat replies to the chat a message came from (DM or group), quoting the
// triggering message.
func (c *Client) sendToChat(ctx context.Context, m *tg.Message, text string) {
	peer, err := c.inputPeer(chatIDFromPeer(m.PeerID))
	if err != nil {
		return
	}
	for _, chunk := range ai.SplitText(text, 4000) {
		sender := message.NewSender(c.api).To(peer)
		builder := &sender.Builder
		if m.ID != 0 {
			builder = builder.Reply(m.ID)
		}
		if _, err := builder.Text(ctx, chunk); err != nil {
			return
		}
	}
}

// chatIDFromPeer converts a gotd peer into the bot-style chat ID used by
// Promobot.
func chatIDFromPeer(p tg.PeerClass) int64 {
	switch p := p.(type) {
	case *tg.PeerUser:
		return p.UserID
	case *tg.PeerChat:
		return -p.ChatID
	case *tg.PeerChannel:
		return -channelPrefix - p.ChannelID
	default:
		return 0
	}
}

// replySticker echoes the received sticker back to the chat.
func (c *Client) replySticker(ctx context.Context, m *tg.Message) {
	doc := messageDoc(m)
	if doc == nil {
		return
	}
	if err := c.sendSticker(chatIDFromPeer(m.PeerID), doc, m.ID); err != nil {
		slog.Warn("sticker echo failed", "err", err.Error())
	}
}

func isSticker(m *tg.Message) bool {
	doc := messageDoc(m)
	if doc == nil {
		return false
	}
	for _, a := range doc.Attributes {
		if _, ok := a.(*tg.DocumentAttributeSticker); ok {
			return true
		}
	}
	return false
}

// messageDoc returns the document of a message's media, if any.
func messageDoc(m *tg.Message) *tg.Document {
	md, ok := m.Media.(*tg.MessageMediaDocument)
	if !ok {
		return nil
	}
	doc, _ := md.Document.(*tg.Document)
	return doc
}
