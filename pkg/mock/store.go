package mock

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Store holds in-memory state per bot token: registered bot identity, recorded
// outbound messages, and webhook configuration. Concurrency-safe; intended for
// load-test workloads with thousands of writers per second.
type Store struct {
	mu sync.RWMutex

	bots          map[string]*botEntry
	messageIDSeed atomic.Int64
	updateIDSeed  atomic.Int64

	Files *FileStore     // multipart upload retention for media proxying
	Proxy *proxyRegistry // outbound forward bindings for real-TG relay
}

type botEntry struct {
	parent *Store // back-ref so AppendMessage can dispatch to proxy registry
	token  string // immutable, set at creation

	identity    User
	webhookURL  string
	secretToken string

	// firstSeen is set once at creation; lastSeen is bumped on every
	// withBot dispatch. Both are unix seconds. lastSeen uses atomic ops
	// since it's written on the hot request path.
	firstSeen int64
	lastSeen  atomic.Int64

	msgMu    sync.RWMutex
	messages []Message // outbound messages from bot, append-only with ring trim

	inputsMu sync.RWMutex
	inputs   []UserInput // text inputs from the debug chat UI (display only)

	// callback_query_id → chat_id map so AnswerCallbackQuery can look up
	// which chat to surface the toast in (the bot only knows the callback
	// id at that point). Capped to avoid unbounded growth under load.
	callbackMu sync.Mutex
	callbacks  map[string]int64

	// Per-chat queue of pending callback toasts (bot's
	// answerCallbackQuery text). Drained by the chat /messages poll.
	toastsMu sync.Mutex
	toasts   map[int64][]Toast

	maxMessages int
	maxInputs   int
}

// Toast is the text the bot wanted to surface via answerCallbackQuery.
// Either a 3s overlay (ShowAlert=false) or a 6s sticky banner styled
// red (ShowAlert=true) in the debug chat UI.
type Toast struct {
	Text      string `json:"text"`
	ShowAlert bool   `json:"show_alert"`
	At        int64  `json:"at"`
}

// UserInput is a text message originated by a human in the debug chat UI.
// Not part of bot outbound; kept only so the chat view can render the
// user's own messages alongside bot replies.
//
// Seq is assigned from the same monotonic counter as Message.MessageID,
// so a unified sort by Seq correctly orders user inputs and bot replies
// even when their second-resolution Date values collide.
type UserInput struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
	Date   int64  `json:"date"`
	Name   string `json:"name"`
	Seq    int    `json:"seq"`
}

const defaultMaxMessages = 5000

// NewStore returns an initialized Store.
func NewStore() *Store {
	s := &Store{
		bots:  make(map[string]*botEntry),
		Files: NewFileStore(),
		Proxy: newProxyRegistry(),
	}
	s.messageIDSeed.Store(time.Now().UnixMilli())
	s.updateIDSeed.Store(time.Now().Unix() * 1000)
	return s
}

// Bot returns the bot entry for a token, auto-registering on first access so
// the mock works without prior configuration: any bot pointed at the mock with
// any token format `<id>:<secret>` will succeed on GetMe.
func (s *Store) Bot(token string) *botEntry {
	s.mu.RLock()
	b := s.bots[token]
	s.mu.RUnlock()
	if b != nil {
		return b
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if b = s.bots[token]; b != nil {
		return b
	}
	now := time.Now().Unix()
	b = &botEntry{
		parent:      s,
		token:       token,
		identity:    identityFromToken(token),
		firstSeen:   now,
		maxMessages: defaultMaxMessages,
		maxInputs:   1000,
	}
	b.lastSeen.Store(now)
	s.bots[token] = b
	return b
}

// touch updates lastSeen on every Bot API request. Called from withBot
// so the inventory page reflects accurate freshness.
func (b *botEntry) touch() { b.lastSeen.Store(time.Now().Unix()) }

// BotInfo is the inventory snapshot returned by /debug/bots and rendered
// in the chat home page. TokenFull is the full token (for click-to-fill);
// TokenShort is the safe-to-display abbreviation.
type BotInfo struct {
	TokenShort   string `json:"token_short"`
	TokenFull    string `json:"token_full"`
	BotID        int64  `json:"bot_id"`
	Username     string `json:"username"`
	WebhookURL   string `json:"webhook_url,omitempty"`
	FirstSeen    int64  `json:"first_seen"`
	LastSeen     int64  `json:"last_seen"`
	MessagesOut  int    `json:"messages_out"`
	ChatsTracked int    `json:"chats_tracked"`
}

// Bots returns a snapshot of every bot the mock has seen since startup,
// sorted by most-recently-active first.
func (s *Store) Bots() []BotInfo {
	s.mu.RLock()
	out := make([]BotInfo, 0, len(s.bots))
	for tok, b := range s.bots {
		b.msgMu.RLock()
		msgs := len(b.messages)
		chats := uniqueChatIDs(b.messages)
		b.msgMu.RUnlock()
		out = append(out, BotInfo{
			TokenShort:   shortenToken(tok),
			TokenFull:    tok,
			BotID:        b.identity.ID,
			Username:     b.identity.Username,
			WebhookURL:   b.webhookURL,
			FirstSeen:    b.firstSeen,
			LastSeen:     b.lastSeen.Load(),
			MessagesOut:  msgs,
			ChatsTracked: chats,
		})
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen > out[j].LastSeen })
	return out
}

func uniqueChatIDs(msgs []Message) int {
	seen := make(map[int64]struct{}, len(msgs))
	for _, m := range msgs {
		seen[m.Chat.ID] = struct{}{}
	}
	return len(seen)
}

// identityFromToken derives a deterministic bot User from the token. Real
// Telegram tokens encode the bot ID before the colon; we mirror that so logs
// look natural.
func identityFromToken(token string) User {
	id := int64(0)
	if idx := strings.IndexByte(token, ':'); idx > 0 {
		id, _ = strconv.ParseInt(token[:idx], 10, 64)
	}
	if id == 0 {
		id = 1
	}
	return User{
		ID:                      id,
		IsBot:                   true,
		FirstName:               "telegym mock bot",
		Username:                "telegym_mock_bot",
		CanJoinGroups:           true,
		CanReadAllGroupMessages: false,
		SupportsInlineQueries:   false,
	}
}

func (s *Store) NextMessageID() int  { return int(s.messageIDSeed.Add(1)) }
func (s *Store) NextUpdateID() int64 { return s.updateIDSeed.Add(1) }

// SetWebhook records a webhook URL plus secret token for the bot.
func (b *botEntry) SetWebhook(url, secret string) {
	b.msgMu.Lock()
	b.webhookURL = url
	b.secretToken = secret
	b.msgMu.Unlock()
}

func (b *botEntry) Webhook() (url, secret string) {
	b.msgMu.RLock()
	url, secret = b.webhookURL, b.secretToken
	b.msgMu.RUnlock()
	return
}

// AppendMessage stores an outbound message and trims to maxMessages.
// Also dispatches to the proxy registry for any (token, chat_id) bound
// to a relay - this is what makes real-Telegram proxying work without
// each handler having to remember to forward.
//
// Every appended entry receives a fresh EventSeq from the global monotonic
// counter, even when MessageID collides (the case for editMessage* --
// the bot edits message X and we store the new state as a separate
// entry with the same MessageID but a higher EventSeq).
func (b *botEntry) AppendMessage(m Message) {
	if b.parent != nil {
		m.EventSeq = int64(b.parent.NextMessageID())
	}
	b.msgMu.Lock()
	b.messages = append(b.messages, m)
	if over := len(b.messages) - b.maxMessages; over > 0 {
		copy(b.messages, b.messages[over:])
		b.messages = b.messages[:b.maxMessages]
	}
	b.msgMu.Unlock()

	if b.parent != nil && b.parent.Proxy != nil {
		b.parent.Proxy.Dispatch(b.token, m)
	}
}

// Messages returns up to `limit` most-recent messages for chatID. If chatID
// is zero, returns messages across all chats.
func (b *botEntry) Messages(chatID int64, limit int) []Message {
	if limit <= 0 {
		limit = 100
	}
	b.msgMu.RLock()
	defer b.msgMu.RUnlock()

	out := make([]Message, 0, limit)
	for i := len(b.messages) - 1; i >= 0 && len(out) < limit; i-- {
		if chatID == 0 || b.messages[i].Chat.ID == chatID {
			out = append(out, b.messages[i])
		}
	}
	return out
}

// MessagesSince returns messages whose Date >= sinceUnix for chatID.
// Used by the xk6 extension to poll for new replies without re-reading all.
func (b *botEntry) MessagesSince(chatID, sinceUnix int64) []Message {
	b.msgMu.RLock()
	defer b.msgMu.RUnlock()

	var out []Message
	for _, m := range b.messages {
		if m.Date < sinceUnix {
			continue
		}
		if chatID != 0 && m.Chat.ID != chatID {
			continue
		}
		out = append(out, m)
	}
	return out
}

// RegisterCallback records callback_query_id → chat_id so that when the
// bot eventually calls answerCallbackQuery with that id, the mock knows
// which chat to surface the toast in. Capped at 5000 entries; once full
// the oldest ~half are dropped (the map order is non-deterministic so
// this is approximate FIFO - good enough for a debug aid).
func (b *botEntry) RegisterCallback(cbID string, chatID int64) {
	if cbID == "" || chatID == 0 {
		return
	}
	b.callbackMu.Lock()
	defer b.callbackMu.Unlock()
	if b.callbacks == nil {
		b.callbacks = map[string]int64{}
	}
	if len(b.callbacks) >= 5000 {
		i := 0
		for k := range b.callbacks {
			delete(b.callbacks, k)
			if i++; i > 2500 {
				break
			}
		}
	}
	b.callbacks[cbID] = chatID
}

func (b *botEntry) LookupCallbackChat(cbID string) int64 {
	b.callbackMu.Lock()
	defer b.callbackMu.Unlock()
	return b.callbacks[cbID]
}

// PushToast queues a callback answer text for display in the given chat.
func (b *botEntry) PushToast(chatID int64, t Toast) {
	b.toastsMu.Lock()
	defer b.toastsMu.Unlock()
	if b.toasts == nil {
		b.toasts = map[int64][]Toast{}
	}
	b.toasts[chatID] = append(b.toasts[chatID], t)
}

// PopToast returns and removes the oldest pending toast for a chat.
// The boolean second return value distinguishes "no toast" from "empty
// toast text" (which we never push but defensively handle anyway).
func (b *botEntry) PopToast(chatID int64) (Toast, bool) {
	b.toastsMu.Lock()
	defer b.toastsMu.Unlock()
	q := b.toasts[chatID]
	if len(q) == 0 {
		return Toast{}, false
	}
	t := q[0]
	b.toasts[chatID] = q[1:]
	return t, true
}

// ClearMessages drops all stored outbound messages for the bot.
func (b *botEntry) ClearMessages() int {
	b.msgMu.Lock()
	n := len(b.messages)
	b.messages = nil
	b.msgMu.Unlock()
	return n
}

// AppendInput records a user-side message from the debug chat UI.
// Stamps a monotonic Seq from the parent store so the chat view can
// order user inputs and bot replies on a single timeline even when
// they share the same one-second Date.
func (b *botEntry) AppendInput(in UserInput) {
	if b.parent != nil {
		in.Seq = b.parent.NextMessageID()
	}
	b.inputsMu.Lock()
	b.inputs = append(b.inputs, in)
	if over := len(b.inputs) - b.maxInputs; over > 0 {
		copy(b.inputs, b.inputs[over:])
		b.inputs = b.inputs[:b.maxInputs]
	}
	b.inputsMu.Unlock()
}

// Inputs returns chronologically-ordered user inputs for chatID.
func (b *botEntry) Inputs(chatID int64) []UserInput {
	b.inputsMu.RLock()
	defer b.inputsMu.RUnlock()
	var out []UserInput
	for _, in := range b.inputs {
		if in.ChatID == chatID {
			out = append(out, in)
		}
	}
	return out
}
