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

	// Chat action from sendChatAction ("typing", "upload_photo", ...) per
	// chat and forum topic. Cleared by the next new message in that topic
	// or after typingTTL.
	typingMu sync.RWMutex
	typing   map[typingKey]chatTyping
	// Forum topics created via createForumTopic, per chat in creation order.
	topicsMu sync.Mutex
	topics   map[int64][]ForumTopic

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
		stored := newest(b.messages, b.maxMessages)
		msgs := len(stored)
		chats := uniqueChatIDs(stored)
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

// AppendMessage stores an outbound message, keeping the newest maxMessages.
// Also dispatches to the proxy registry for any (token, chat_id) bound
// to a relay - this is what makes real-Telegram proxying work without
// each handler having to remember to forward.
//
// Every appended entry receives a fresh EventSeq from the global monotonic
// counter, even when MessageID collides (the case for editMessage* --
// the bot edits message X and we store the new state as a separate
// entry with the same MessageID but a higher EventSeq).
func (b *botEntry) AppendMessage(m Message) {
	if m.isNew {
		b.ClearTyping(m.Chat.ID, m.MessageThreadID)
	}
	b.storeMessage(m)

	if b.parent != nil && b.parent.Proxy != nil {
		b.parent.Proxy.Dispatch(b.token, m)
	}
}

func (b *botEntry) storeMessage(m Message) {
	if b.parent != nil {
		m.EventSeq = int64(b.parent.NextMessageID())
	}
	b.msgMu.Lock()
	b.messages = appendCapped(b.messages, m, b.maxMessages)
	b.msgMu.Unlock()
}

// CreateForumTopic opens a new topic in chatID. Like Telegram, every call
// creates a new topic (names need not be unique) and the topic's
// message_thread_id is the message_id of its forum_topic_created service
// message, which is stored in the topic thread. The service message is
// not relayed to the proxy - real Telegram creates it itself.
func (b *botEntry) CreateForumTopic(chatID int64, name string, iconColor int, iconEmojiID string) ForumTopic {
	threadID := 1
	if b.parent != nil {
		threadID = b.parent.NextMessageID()
	}
	topic := ForumTopic{MessageThreadID: threadID, Name: name, IconColor: iconColor, IconCustomEmojiID: iconEmojiID}

	b.topicsMu.Lock()
	if b.topics == nil {
		b.topics = map[int64][]ForumTopic{}
	}
	b.topics[chatID] = append(b.topics[chatID], topic)
	b.topicsMu.Unlock()

	b.storeMessage(Message{
		MessageID:       threadID,
		From:            &b.identity,
		Date:            time.Now().Unix(),
		Chat:            Chat{ID: chatID, Type: "private"},
		MessageThreadID: threadID,
		IsTopicMessage:  true,
		ForumTopicCreated: &ForumTopicCreated{
			Name: name, IconColor: iconColor, IconCustomEmojiID: iconEmojiID,
		},
	})
	return topic
}

// ForumTopics lists the topics of chatID in creation order.
func (b *botEntry) ForumTopics(chatID int64) []ForumTopic {
	b.topicsMu.Lock()
	defer b.topicsMu.Unlock()
	return append([]ForumTopic(nil), b.topics[chatID]...)
}

// DeleteMessage removes every stored entry for chatID+messageID, including
// earlier edit revisions, so the bubble disappears from /debug/chat.
// Returns false when nothing matched (e.g. a user message or an entry
// already evicted from the buffer).
func (b *botEntry) DeleteMessage(chatID int64, messageID int) bool {
	b.msgMu.Lock()
	defer b.msgMu.Unlock()
	kept := b.messages[:0]
	for _, m := range b.messages {
		if m.Chat.ID == chatID && m.MessageID == messageID {
			continue
		}
		kept = append(kept, m)
	}
	removed := len(kept) != len(b.messages)
	clear(b.messages[len(kept):])
	b.messages = kept
	return removed
}

// Messages returns up to `limit` most-recent messages for chatID. If chatID
// is zero, returns messages across all chats.
func (b *botEntry) Messages(chatID int64, limit int) []Message {
	if limit <= 0 {
		limit = 100
	}
	b.msgMu.RLock()
	defer b.msgMu.RUnlock()

	stored := newest(b.messages, b.maxMessages)
	out := make([]Message, 0, limit)
	for i := len(stored) - 1; i >= 0 && len(out) < limit; i-- {
		if chatID == 0 || stored[i].Chat.ID == chatID {
			out = append(out, stored[i])
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
	for _, m := range newest(b.messages, b.maxMessages) {
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

// Telegram shows a chat action for up to 5 seconds or until the bot's next
// message. typingMinVisible keeps the bubble on screen briefly even when the
// message follows immediately, so the debug chat poll (every 500ms) sees it.
const (
	typingTTL        = 5 * time.Second
	typingMinVisible = time.Second
)

// typingKey scopes a chat action to a chat and forum topic (thread 0 =
// General or a chat without topics).
type typingKey struct {
	chatID int64
	thread int
}

type chatTyping struct {
	action string
	since  time.Time
	until  time.Time
}

// ChatAction is an active sendChatAction in one topic of a chat.
type ChatAction struct {
	Thread int
	Action string
}

// SetTyping records a sendChatAction for chatID in the given topic.
func (b *botEntry) SetTyping(chatID int64, thread int, action string) {
	now := time.Now()
	b.typingMu.Lock()
	defer b.typingMu.Unlock()
	if b.typing == nil {
		b.typing = map[typingKey]chatTyping{}
	}
	b.typing[typingKey{chatID, thread}] = chatTyping{action: action, since: now, until: now.Add(typingTTL)}
}

// ClearTyping ends the chat action in one topic of chatID, keeping it
// until the typingMinVisible window has passed.
func (b *botEntry) ClearTyping(chatID int64, thread int) {
	key := typingKey{chatID, thread}
	// Every send calls this; skip the write lock when nothing is typing so
	// concurrent sends do not serialize here.
	b.typingMu.RLock()
	_, ok := b.typing[key]
	b.typingMu.RUnlock()
	if !ok {
		return
	}
	b.typingMu.Lock()
	defer b.typingMu.Unlock()
	t, ok := b.typing[key]
	if !ok {
		return
	}
	if minUntil := t.since.Add(typingMinVisible); time.Now().Before(minUntil) {
		t.until = minUntil
		b.typing[key] = t
		return
	}
	delete(b.typing, key)
}

// ActiveTyping returns the unexpired chat actions of chatID, ordered by
// topic.
func (b *botEntry) ActiveTyping(chatID int64) []ChatAction {
	now := time.Now()
	b.typingMu.Lock()
	defer b.typingMu.Unlock()
	var out []ChatAction
	for key, t := range b.typing {
		if !now.Before(t.until) {
			delete(b.typing, key)
			continue
		}
		if key.chatID == chatID {
			out = append(out, ChatAction{Thread: key.thread, Action: t.action})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Thread < out[j].Thread })
	return out
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
	b.inputs = appendCapped(b.inputs, in, b.maxInputs)
	b.inputsMu.Unlock()
}

// Inputs returns chronologically-ordered user inputs for chatID.
func (b *botEntry) Inputs(chatID int64) []UserInput {
	b.inputsMu.RLock()
	defer b.inputsMu.RUnlock()
	var out []UserInput
	for _, in := range newest(b.inputs, b.maxInputs) {
		if in.ChatID == chatID {
			out = append(out, in)
		}
	}
	return out
}

// appendCapped appends v to a buffer that keeps the newest limit entries.
// The slice grows to 2*limit before the oldest entries are dropped in a single
// copy, so appends stay O(1) amortized instead of shifting the whole buffer
// on every call once it is full. Readers see the newest limit via newest.
func appendCapped[T any](s []T, v T, limit int) []T {
	s = append(s, v)
	if len(s) > 2*limit {
		n := copy(s, s[len(s)-limit:])
		clear(s[n:])
		s = s[:n]
	}
	return s
}

// newest returns the last limit entries of a buffer filled by appendCapped.
func newest[T any](s []T, limit int) []T {
	if len(s) > limit {
		return s[len(s)-limit:]
	}
	return s
}
