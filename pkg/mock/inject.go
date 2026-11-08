package mock

import (
	"errors"
	"strconv"
	"time"
)

// fillUpdate builds the injected update types that leave no state in the mock.
// Its errors are the caller's fault and answered with a 400.
func (h *Handlers) fillUpdate(upd *Update, bot *botEntry, req *InjectUpdateRequest, from User, chat Chat) error {
	now := time.Now().Unix()
	switch req.Type {
	case "my_chat_member":
		if !supportedMemberStatus(req.OldStatus) || !supportedMemberStatus(req.NewStatus) {
			return errors.New("old_status and new_status must be member, left or kicked")
		}
		upd.MyChatMember = &ChatMemberUpdated{
			Chat:          chat,
			From:          from,
			Date:          now,
			OldChatMember: chatMemberWithStatus(bot.identity, req.OldStatus),
			NewChatMember: chatMemberWithStatus(bot.identity, req.NewStatus),
		}
	case "inline_query":
		upd.InlineQuery = &InlineQuery{
			ID:       h.Store.NextQueryID(),
			From:     from,
			Query:    req.Query,
			Offset:   req.Offset,
			ChatType: req.ChatType,
		}
	case "chosen_inline_result":
		if req.ResultID == "" {
			return errors.New("result_id is required")
		}
		upd.ChosenInlineResult = &ChosenInlineResult{ResultID: req.ResultID, From: from, Query: req.Query}
	case "pre_checkout_query":
		if err := normalizePayment(req); err != nil {
			return err
		}
		upd.PreCheckoutQuery = &PreCheckoutQuery{
			ID:             h.Store.NextQueryID(),
			From:           from,
			Currency:       req.Currency,
			TotalAmount:    req.TotalAmount,
			InvoicePayload: req.InvoicePayload,
		}
	case "successful_payment":
		if err := normalizePayment(req); err != nil {
			return err
		}
		upd.Message = &Message{
			MessageID: bot.NextMessageID(chat.ID),
			From:      &from,
			Date:      now,
			Chat:      chat,
			SuccessfulPayment: &SuccessfulPayment{
				Currency:                req.Currency,
				TotalAmount:             req.TotalAmount,
				InvoicePayload:          req.InvoicePayload,
				TelegramPaymentChargeID: "telegram_charge_" + h.Store.NextQueryID(),
				ProviderPaymentChargeID: "provider_charge_" + h.Store.NextQueryID(),
			},
		}
	case "message_reaction":
		if req.MessageID == 0 {
			return errors.New("message_id is required")
		}
		if len(req.OldReaction) == 0 && len(req.NewReaction) == 0 {
			return errors.New("old_reaction or new_reaction is required")
		}
		upd.MessageReaction = &MessageReactionUpdated{
			Chat:        chat,
			MessageID:   req.MessageID,
			User:        &from,
			Date:        now,
			OldReaction: emojiReactions(req.OldReaction),
			NewReaction: emojiReactions(req.NewReaction),
		}
	case "poll_answer":
		if req.PollID == "" {
			return errors.New("poll_id is required")
		}
		// Both option arrays are required fields, even for a retracted vote.
		persistentIDs := make([]string, 0, len(req.OptionIDs))
		for _, option := range req.OptionIDs {
			persistentIDs = append(persistentIDs, strconv.Itoa(option))
		}
		upd.PollAnswer = &PollAnswer{
			PollID:              req.PollID,
			User:                &from,
			OptionIDs:           append([]int{}, req.OptionIDs...),
			OptionPersistentIDs: persistentIDs,
		}
	default:
		return errors.New("unsupported update type")
	}
	return nil
}

func normalizePayment(req *InjectUpdateRequest) error {
	if req.TotalAmount <= 0 {
		return errors.New("total_amount must be positive")
	}
	if req.InvoicePayload == "" {
		return errors.New("invoice_payload is required")
	}
	if req.Currency == "" {
		req.Currency = "XTR"
	}
	return nil
}

// emojiReactions never returns nil: both reaction arrays are required fields.
func emojiReactions(emojis []string) []ReactionType {
	reactions := make([]ReactionType, 0, len(emojis))
	for _, emoji := range emojis {
		reactions = append(reactions, ReactionType{Type: "emoji", Emoji: emoji})
	}
	return reactions
}

// attachMedia copies the requested media onto message, filling the fields
// Telegram always sets, and refuses combinations Telegram cannot produce.
func attachMedia(message *Message, req *InjectUpdateRequest) error {
	captioned := len(req.Photo) > 0 || req.Document != nil
	plain := req.Contact != nil || req.Location != nil || req.Sticker != nil
	if req.Caption != "" && !captioned {
		return errors.New("caption requires photo or document")
	}
	if !captioned && !plain {
		return nil
	}
	if req.Text != "" {
		return errors.New("media messages carry a caption, not text")
	}
	if countSet(len(req.Photo) > 0, req.Document != nil, req.Contact != nil, req.Location != nil, req.Sticker != nil) > 1 {
		return errors.New("send at most one of photo, document, contact, location, sticker")
	}

	message.Caption = req.Caption
	for i := range req.Photo {
		size := &req.Photo[i]
		if size.FileID == "" {
			return errors.New("photo file_id is required")
		}
		if size.FileUniqueID == "" {
			size.FileUniqueID = size.FileID
		}
		if size.Width == 0 {
			size.Width = 1280
		}
		if size.Height == 0 {
			size.Height = 720
		}
	}
	message.Photo = req.Photo

	if doc := req.Document; doc != nil {
		if doc.FileID == "" {
			return errors.New("document file_id is required")
		}
		if doc.FileUniqueID == "" {
			doc.FileUniqueID = doc.FileID
		}
		message.Document = doc
	}
	if contact := req.Contact; contact != nil {
		if contact.PhoneNumber == "" {
			return errors.New("contact phone_number is required")
		}
		if contact.FirstName == "" {
			contact.FirstName = req.FirstName
		}
		message.Contact = contact
	}
	if sticker := req.Sticker; sticker != nil {
		if sticker.FileID == "" {
			return errors.New("sticker file_id is required")
		}
		if sticker.FileUniqueID == "" {
			sticker.FileUniqueID = sticker.FileID
		}
		if sticker.Type == "" {
			sticker.Type = "regular"
		}
		if sticker.Width == 0 {
			sticker.Width = 512
		}
		if sticker.Height == 0 {
			sticker.Height = 512
		}
		message.Sticker = sticker
	}
	message.Location = req.Location
	return nil
}

func countSet(flags ...bool) int {
	n := 0
	for _, set := range flags {
		if set {
			n++
		}
	}
	return n
}
