package assistant

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"promobot/internal/ai"
	"promobot/internal/config"
	"promobot/internal/store"
)

// SettingKey is the legacy store key under which a single assistant session was
// saved. New accounts are stored in the assistant_accounts collection.
const SettingKey = "assistant_session"

// MaxAccounts is how many assistant accounts a single owner may connect.
const MaxAccounts = 5

// ErrNotConnected is returned when an assistant action is attempted while no
// session is connected.
var ErrNotConnected = fmt.Errorf("assistant is not connected")

// AccountInfo describes a connected assistant account.
type AccountInfo struct {
	ID      string
	UserID  int64
	AddedAt int64
}

type account struct {
	info   AccountInfo
	client *Client
	aic    *ai.Client
}

// Manager owns the assistant lifecycle so accounts can be connected, listed and
// removed at runtime (via /setstring, /accounts and /delstring) without a
// restart. Each account keeps its own AI memory namespace, so conversation
// history is isolated per account and per user.
type Manager struct {
	cfg    *config.Config
	st     store.Store
	baseAI *ai.Client
	mem    ai.Memory

	mu       sync.RWMutex
	accounts map[string]*account
	order    []string
	mood     MoodStickerProvider
	// personaName is the effective trigger/identity name, overridable at runtime
	// via /persona so new accounts pick it up too.
	personaName string
}

// NewManager constructs a Manager.
func NewManager(cfg *config.Config, st store.Store, aiClient *ai.Client) *Manager {
	m := &Manager{
		cfg:         cfg,
		st:          st,
		baseAI:      aiClient,
		accounts:    make(map[string]*account),
		personaName: cfg.PersonaName,
	}
	if st != nil {
		m.mem = st
	}
	return m
}

// SetMoodStickerProvider installs the mood sticker provider on the manager and
// all connected accounts.
func (m *Manager) SetMoodStickerProvider(p MoodStickerProvider) {
	m.mu.Lock()
	m.mood = p
	for _, a := range m.accounts {
		a.client.SetMoodStickerProvider(p)
	}
	m.mu.Unlock()
}

// AddSession validates a session string, connects it and registers the account.
// The account id is the account's Telegram user id. An already-connected
// account is replaced. It returns the account id and user id.
func (m *Manager) AddSession(ctx context.Context, session string) (string, int64, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return "", 0, fmt.Errorf("empty session string")
	}
	if _, _, _, _, err := decodePyrogramSession(session); err != nil {
		return "", 0, fmt.Errorf("invalid session string: %w", err)
	}

	m.mu.RLock()
	mood := m.mood
	personaName := m.personaName
	m.mu.RUnlock()

	aic := m.baseAI.Clone()
	client, err := FromPyrogramSession(int(m.cfg.APIID), m.cfg.APIHash, session, aic)
	if err != nil {
		return "", 0, err
	}
	client.SetPersona(personaName)
	client.SetStore(m.st)
	if mood != nil {
		client.SetMoodStickerProvider(mood)
	}

	selfID := client.SelfID()
	id := strconv.FormatInt(selfID, 10)
	if aic != nil && m.mem != nil {
		aic.SetMemory(m.mem, id)
	}

	m.mu.Lock()
	existing := m.accounts[id]
	if existing == nil && len(m.accounts) >= MaxAccounts {
		m.mu.Unlock()
		client.Close()
		return "", 0, fmt.Errorf("maximum of %d accounts reached; remove one first", MaxAccounts)
	}
	acc := &account{
		info:   AccountInfo{ID: id, UserID: selfID, AddedAt: time.Now().Unix()},
		client: client,
		aic:    aic,
	}
	if existing == nil {
		m.order = append(m.order, id)
	} else {
		existing.client.Close()
	}
	m.accounts[id] = acc
	m.mu.Unlock()

	if m.st != nil {
		if err := m.st.AddAccount(ctx, store.AssistantAccount{
			ID:      id,
			Session: session,
			UserID:  selfID,
			AddedAt: acc.info.AddedAt,
		}); err != nil {
			return id, selfID, fmt.Errorf("connected, but saving the account failed: %w", err)
		}
	}
	return id, selfID, nil
}

// SetSystem updates the AI system prompt on the base client and every connected
// account, so a persona change applies without reconnecting.
func (m *Manager) SetSystem(system string) {
	if m.baseAI != nil {
		m.baseAI.SetSystem(system)
	}
	m.mu.Lock()
	for _, a := range m.accounts {
		if a.aic != nil {
			a.aic.SetSystem(system)
		}
	}
	m.mu.Unlock()
}

// SetPersonaName changes the group trigger/identity name for the manager and all
// connected accounts. An empty name falls back to the configured persona.
func (m *Manager) SetPersonaName(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = m.cfg.PersonaName
	}
	m.mu.Lock()
	m.personaName = name
	for _, a := range m.accounts {
		a.client.SetPersona(name)
	}
	m.mu.Unlock()
}

// LeaveChat makes the primary assistant leave a single named chat.
func (m *Manager) LeaveChat(chatID int64) error {
	c := m.current()
	if c == nil {
		return ErrNotConnected
	}
	return c.LeaveChat(chatID)
}

// RemoveSession disconnects and forgets one account, or every account when id
// is empty.
func (m *Manager) RemoveSession(ctx context.Context, id string) error {
	m.mu.Lock()
	var removed []*Client
	if id == "" {
		for _, a := range m.accounts {
			removed = append(removed, a.client)
		}
		m.accounts = make(map[string]*account)
		m.order = nil
	} else if a, ok := m.accounts[id]; ok {
		removed = append(removed, a.client)
		delete(m.accounts, id)
		m.order = removeID(m.order, id)
	}
	m.mu.Unlock()

	for _, c := range removed {
		c.Close()
	}
	if m.st != nil {
		if id == "" {
			_, err := m.st.DeleteAllAccounts(ctx)
			return err
		}
		return m.st.DeleteAccount(ctx, id)
	}
	return nil
}

// LoadStored connects every account persisted in the store.
func (m *Manager) LoadStored(ctx context.Context) error {
	if m.st == nil {
		return nil
	}
	accs, err := m.st.ListAccounts(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, a := range accs {
		if _, _, err := m.AddSession(ctx, a.Session); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Connected reports whether at least one account is connected.
func (m *Manager) Connected() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.accounts) > 0
}

// SelfID returns the primary account's user id, or 0 when none is connected.
func (m *Manager) SelfID() int64 {
	if a := m.primary(); a != nil {
		return a.info.UserID
	}
	return 0
}

// Accounts returns the connected accounts in insertion order.
func (m *Manager) Accounts() []AccountInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]AccountInfo, 0, len(m.order))
	for _, id := range m.order {
		if a, ok := m.accounts[id]; ok {
			out = append(out, a.info)
		}
	}
	return out
}

// AccountLines returns one display line per connected account.
func (m *Manager) AccountLines() []string {
	accs := m.Accounts()
	out := make([]string, 0, len(accs))
	for i, a := range accs {
		out = append(out, fmt.Sprintf("%d. id=%s user=%d", i+1, a.ID, a.UserID))
	}
	return out
}

func (m *Manager) primary() *account {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, id := range m.order {
		if a, ok := m.accounts[id]; ok {
			return a
		}
	}
	return nil
}

func (m *Manager) current() *Client {
	if a := m.primary(); a != nil {
		return a.client
	}
	return nil
}

func removeID(ids []string, id string) []string {
	out := ids[:0]
	for _, v := range ids {
		if v != id {
			out = append(out, v)
		}
	}
	return out
}

// The methods below implement bot.Assistant by delegating to the primary client.

// ResolveChat resolves a target to a chat id.
func (m *Manager) ResolveChat(target string) (int64, error) {
	c := m.current()
	if c == nil {
		return 0, ErrNotConnected
	}
	return c.ResolveChat(target)
}

// IsAdmin reports whether the assistant is an admin of the channel.
func (m *Manager) IsAdmin(chatID int64) (bool, error) {
	c := m.current()
	if c == nil {
		return false, ErrNotConnected
	}
	return c.IsAdmin(chatID)
}

// SendText sends a text message as the assistant.
func (m *Manager) SendText(chatID int64, text string) error {
	c := m.current()
	if c == nil {
		return ErrNotConnected
	}
	return c.SendText(chatID, text)
}

// SendMedia sends a media attachment as the assistant.
func (m *Manager) SendMedia(chatID int64, media store.Media) error {
	c := m.current()
	if c == nil {
		return ErrNotConnected
	}
	return c.SendMedia(chatID, media)
}

// ForwardMessage forwards a message from fromChatID to chatID as the primary
// assistant, preserving the "Forwarded from" header.
func (m *Manager) ForwardMessage(chatID, fromChatID int64, msgID int, silent bool) (int, error) {
	c := m.current()
	if c == nil {
		return 0, ErrNotConnected
	}
	return c.ForwardMessage(chatID, fromChatID, msgID, silent)
}

// EditCaption updates the caption/text of a message the assistant sent.
func (m *Manager) EditCaption(chatID int64, msgID int, caption string) error {
	c := m.current()
	if c == nil {
		return ErrNotConnected
	}
	return c.EditCaption(chatID, msgID, caption)
}

// PinMessage pins a message in a chat as the assistant.
func (m *Manager) PinMessage(chatID int64, msgID int, silent bool) error {
	c := m.current()
	if c == nil {
		return ErrNotConnected
	}
	return c.PinMessage(chatID, msgID, silent)
}

// KangStickerFile uploads raw sticker bytes into the assistant's pack.
func (m *Manager) KangStickerFile(ctx context.Context, name string, data []byte, mime, emoji string) (string, error) {
	c := m.current()
	if c == nil {
		return "", ErrNotConnected
	}
	return c.KangStickerFile(ctx, name, data, mime, emoji)
}

// AutoStickerPacks returns display lines for the primary assistant's auto packs.
func (m *Manager) AutoStickerPacks() []string {
	c := m.current()
	if c == nil {
		return nil
	}
	return c.AutoStickerPacks()
}

// Close disconnects every account.
func (m *Manager) Close() {
	m.mu.Lock()
	accs := m.accounts
	m.accounts = make(map[string]*account)
	m.order = nil
	m.mu.Unlock()
	for _, a := range accs {
		a.client.Close()
	}
}
