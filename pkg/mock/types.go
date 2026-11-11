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
	// Parameters carries retry_after on 429 responses, as in the Bot API.
	Parameters *ResponseParameters `json:"parameters,omitempty"`
}

// ResponseParameters explains why a request failed (subset of the Bot API type).
type ResponseParameters struct {
	RetryAfter int `json:"retry_after,omitempty"`
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
	MessageID       int          `json:"message_id"`
	From            *User        `json:"from,omitempty"`
	Date            int64        `json:"date"`
	Chat            Chat         `json:"chat"`
	MessageThreadID int          `json:"message_thread_id,omitempty"`
	IsTopicMessage  bool         `json:"is_topic_message,omitempty"`
	Text            string       `json:"text,omitempty"`
	Caption         string       `json:"caption,omitempty"`
	Entities        []Entity     `json:"entities,omitempty"`
	CaptionEntities []Entity     `json:"caption_entities,omitempty"`
	ReplyMarkup     *ReplyMarkup `json:"reply_markup,omitempty"`
	ReplyToMessage  *Message     `json:"reply_to_message,omitempty"`

	// ParseMode is the parse_mode the bot sent with text or caption. Mock
	// internal: real Telegram returns parsed entities instead.
	ParseMode string `json:"-"`

	Photo     []PhotoSize `json:"photo,omitempty"`
	Video     *Video      `json:"video,omitempty"`
	Animation *Animation  `json:"animation,omitempty"`
	Sticker   *Sticker    `json:"sticker,omitempty"`
	Dice      *Dice       `json:"dice,omitempty"`
	Document  *Document   `json:"document,omitempty"`
	Contact   *Contact    `json:"contact,omitempty"`
	Location  *Location   `json:"location,omitempty"`

	SuccessfulPayment *SuccessfulPayment `json:"successful_payment,omitempty"`

	ForumTopicCreated *ForumTopicCreated `json:"forum_topic_created,omitempty"`
	RichMessage       *RichMessage       `json:"rich_message,omitempty"`

	// EventSeq is a mock-internal monotonic counter, incremented on every
	// AppendMessage. Two entries with the same MessageID (a bot send
	// followed by an edit of that message) get DIFFERENT EventSeq values,
	// so pollers can distinguish them. Real Telegram has no analogous
	// field; clients that don't care can ignore it.
	EventSeq int64 `json:"_event_seq,omitempty"`

	// isNew marks a message from a send* method (not an edit): storing it
	// ends the sendChatAction status in its topic.
	isNew bool
}

// ForumTopic is the createForumTopic result.
type ForumTopic struct {
	MessageThreadID   int    `json:"message_thread_id"`
	Name              string `json:"name"`
	IconColor         int    `json:"icon_color"`
	IconCustomEmojiID string `json:"icon_custom_emoji_id,omitempty"`
}

// ForumTopicCreated is the service message content that opens a new topic.
type ForumTopicCreated struct {
	Name              string `json:"name"`
	IconColor         int    `json:"icon_color"`
	IconCustomEmojiID string `json:"icon_custom_emoji_id,omitempty"`
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
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type Animation struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Duration     int    `json:"duration"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
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
	FileSize     int64  `json:"file_size,omitempty"`
}

type Dice struct {
	Emoji string `json:"emoji"`
	Value int    `json:"value"`
}

type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type Contact struct {
	PhoneNumber string `json:"phone_number"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name,omitempty"`
	UserID      int64  `json:"user_id,omitempty"`
	VCard       string `json:"vcard,omitempty"`
}

type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type SuccessfulPayment struct {
	Currency                string `json:"currency"`
	TotalAmount             int    `json:"total_amount"`
	InvoicePayload          string `json:"invoice_payload"`
	TelegramPaymentChargeID string `json:"telegram_payment_charge_id"`
	ProviderPaymentChargeID string `json:"provider_payment_charge_id"`
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
	// UntilDate is required for kicked members, where 0 means forever.
	UntilDate *int64 `json:"until_date,omitempty"`
}

type ChatMemberUpdated struct {
	Chat          Chat       `json:"chat"`
	From          User       `json:"from"`
	Date          int64      `json:"date"`
	OldChatMember ChatMember `json:"old_chat_member"`
	NewChatMember ChatMember `json:"new_chat_member"`
}

type InlineQuery struct {
	ID       string `json:"id"`
	From     User   `json:"from"`
	Query    string `json:"query"`
	Offset   string `json:"offset"`
	ChatType string `json:"chat_type,omitempty"`
}

type ChosenInlineResult struct {
	ResultID string `json:"result_id"`
	From     User   `json:"from"`
	Query    string `json:"query"`
}

type PreCheckoutQuery struct {
	ID             string `json:"id"`
	From           User   `json:"from"`
	Currency       string `json:"currency"`
	TotalAmount    int    `json:"total_amount"`
	InvoicePayload string `json:"invoice_payload"`
}

type ReactionType struct {
	Type  string `json:"type"`
	Emoji string `json:"emoji"`
}

type MessageReactionUpdated struct {
	Chat        Chat           `json:"chat"`
	MessageID   int            `json:"message_id"`
	User        *User          `json:"user,omitempty"`
	Date        int64          `json:"date"`
	OldReaction []ReactionType `json:"old_reaction"`
	NewReaction []ReactionType `json:"new_reaction"`
}

type PollAnswer struct {
	PollID              string   `json:"poll_id"`
	User                *User    `json:"user,omitempty"`
	OptionIDs           []int    `json:"option_ids"`
	OptionPersistentIDs []string `json:"option_persistent_ids"`
}

type Update struct {
	UpdateID           int64                   `json:"update_id"`
	Message            *Message                `json:"message,omitempty"`
	EditedMessage      *Message                `json:"edited_message,omitempty"`
	CallbackQuery      *CallbackQuery          `json:"callback_query,omitempty"`
	ChatMember         *ChatMemberUpdated      `json:"chat_member,omitempty"`
	MyChatMember       *ChatMemberUpdated      `json:"my_chat_member,omitempty"`
	InlineQuery        *InlineQuery            `json:"inline_query,omitempty"`
	ChosenInlineResult *ChosenInlineResult     `json:"chosen_inline_result,omitempty"`
	PreCheckoutQuery   *PreCheckoutQuery       `json:"pre_checkout_query,omitempty"`
	MessageReaction    *MessageReactionUpdated `json:"message_reaction,omitempty"`
	PollAnswer         *PollAnswer             `json:"poll_answer,omitempty"`
}

// WebhookInfo mirrors the response of getWebhookInfo.
type WebhookInfo struct {
	URL                  string `json:"url"`
	HasCustomCertificate bool   `json:"has_custom_certificate"`
	PendingUpdateCount   int    `json:"pending_update_count"`
	MaxConnections       int    `json:"max_connections,omitempty"`
}
