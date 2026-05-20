// Package mock implements a drop-in Telegram Bot API server for use in
// integration and load tests. It accepts the same HTTP requests as
// api.telegram.org, returns shape-compatible JSON, and forwards injected
// updates to the bot's webhook URL.
package mock

// APIResponse is the envelope returned by every Bot API method.
// See: https://core.telegram.org/bots/api#making-requests
type APIResponse struct {
	OK          bool        `json:"ok"`
	Result      interface{} `json:"result,omitempty"`
	Description string      `json:"description,omitempty"`
	ErrorCode   int         `json:"error_code,omitempty"`
}

type User struct {
	ID                      int64  `json:"id"`
	IsBot                   bool   `json:"is_bot"`
	FirstName               string `json:"first_name"`
	LastName                string `json:"last_name,omitempty"`
	Username                string `json:"username,omitempty"`
	LanguageCode            string `json:"language_code,omitempty"`
	CanJoinGroups           bool   `json:"can_join_groups,omitempty"`
	CanReadAllGroupMessages bool   `json:"can_read_all_group_messages,omitempty"`
	SupportsInlineQueries   bool   `json:"supports_inline_queries,omitempty"`
}

type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
	Title     string `json:"title,omitempty"`
}

type Message struct {
	MessageID   int          `json:"message_id"`
	From        *User        `json:"from,omitempty"`
	Date        int64        `json:"date"`
	Chat        Chat         `json:"chat"`
	Text        string       `json:"text,omitempty"`
	Caption     string       `json:"caption,omitempty"`
	Entities    []Entity     `json:"entities,omitempty"`
	ReplyMarkup *ReplyMarkup `json:"reply_markup,omitempty"`

	Photo     []PhotoSize `json:"photo,omitempty"`
	Video     *Video      `json:"video,omitempty"`
	Animation *Animation  `json:"animation,omitempty"`
	Sticker   *Sticker    `json:"sticker,omitempty"`
	Dice      *Dice       `json:"dice,omitempty"`

	// EventSeq is a mock-internal monotonic counter, incremented on every
	// AppendMessage. Two entries with the same MessageID (a bot send
	// followed by an edit of that message) get DIFFERENT EventSeq values,
	// so pollers can distinguish them. Real Telegram has no analogous
	// field; clients that don't care can ignore it.
	EventSeq int64 `json:"_event_seq,omitempty"`
}

type Entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url,omitempty"`
}

// ReplyMarkup unmarshals any of the four Bot API markup shapes into a
// single struct (their JSON fields are disjoint). Inline keyboards live
// inside the message bubble; reply keyboards / remove / force-reply
// affect the chat-level input area.
type ReplyMarkup struct {
	InlineKeyboard [][]InlineButton `json:"inline_keyboard,omitempty"`

	// ReplyKeyboardMarkup
	Keyboard              [][]KeyboardButton `json:"keyboard,omitempty"`
	ResizeKeyboard        bool               `json:"resize_keyboard,omitempty"`
	OneTimeKeyboard       bool               `json:"one_time_keyboard,omitempty"`
	IsPersistent          bool               `json:"is_persistent,omitempty"`
	InputFieldPlaceholder string             `json:"input_field_placeholder,omitempty"`
	Selective             bool               `json:"selective,omitempty"`

	// ReplyKeyboardRemove
	RemoveKeyboard bool `json:"remove_keyboard,omitempty"`

	// ForceReply
	ForceReply bool `json:"force_reply,omitempty"`
}

type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
	// Style: "primary" (blue), "success" (green), "danger" (red). Empty = default.
	Style string `json:"style,omitempty"`
}

// KeyboardButton represents a reply-keyboard cell. Only Text is required;
// optional request_* fields (contact/location/user/poll/web_app) are
// captured generically so the mock doesn't drop them on round-trip.
type KeyboardButton struct {
	Text            string         `json:"text"`
	RequestContact  bool           `json:"request_contact,omitempty"`
	RequestLocation bool           `json:"request_location,omitempty"`
	RequestUser     map[string]any `json:"request_user,omitempty"`
	RequestPoll     map[string]any `json:"request_poll,omitempty"`
	WebApp          map[string]any `json:"web_app,omitempty"`
}

type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int    `json:"file_size,omitempty"`
}

type Video struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Duration     int    `json:"duration"`
}

type Animation struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Duration     int    `json:"duration"`
}

type Sticker struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Type         string `json:"type"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	IsAnimated   bool   `json:"is_animated"`
	IsVideo      bool   `json:"is_video"`
	Emoji        string `json:"emoji,omitempty"`
}

type Dice struct {
	Emoji string `json:"emoji"`
	Value int    `json:"value"`
}

type CallbackQuery struct {
	ID           string   `json:"id"`
	From         User     `json:"from"`
	Message      *Message `json:"message,omitempty"`
	ChatInstance string   `json:"chat_instance"`
	Data         string   `json:"data,omitempty"`
}

type ChatMember struct {
	Status string `json:"status"`
	User   User   `json:"user"`
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	EditedMessage *Message       `json:"edited_message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// WebhookInfo mirrors the response of getWebhookInfo.
type WebhookInfo struct {
	URL                  string `json:"url"`
	HasCustomCertificate bool   `json:"has_custom_certificate"`
	PendingUpdateCount   int    `json:"pending_update_count"`
	MaxConnections       int    `json:"max_connections,omitempty"`
}
