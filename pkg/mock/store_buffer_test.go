package mock

import (
	"fmt"
	"testing"
)

func TestMessageBufferKeepsNewest(t *testing.T) {
	b := NewStore().Bot("741:buffer")
	b.maxMessages = 10
	b.maxInputs = 10
	for i := 1; i <= 35; i++ {
		b.AppendMessage(Message{MessageID: i, Chat: Chat{ID: 42}, Date: int64(i)})
		b.AppendInput(UserInput{ChatID: 42, Text: fmt.Sprint(i)})
	}

	msgs := b.Messages(42, 100)
	if len(msgs) != 10 || msgs[0].MessageID != 35 || msgs[9].MessageID != 26 {
		t.Fatalf("Messages must return the newest 10, newest first: %+v", msgs)
	}
	if since := b.MessagesSince(42, 0); len(since) != 10 || since[0].MessageID != 26 {
		t.Fatalf("MessagesSince must see only the newest 10: %+v", since)
	}
	if in := b.Inputs(42); len(in) != 10 || in[0].Text != "26" || in[9].Text != "35" {
		t.Fatalf("Inputs must return the newest 10 in order: %+v", in)
	}
	if len(b.messages) > 2*b.maxMessages {
		t.Fatalf("buffer grew past 2*max: %d", len(b.messages))
	}
}

// BenchmarkSendMessageFullStore measures sendMessage once the per-bot
// history is at its cap, the steady state of a long load test.
func BenchmarkSendMessageFullStore(b *testing.B) {
	srv := New(Config{Quiet: true})
	h := srv.Handler()
	bot := srv.handlers.Store.Bot("741:bench-full")
	for i := range defaultMaxMessages {
		bot.AppendMessage(Message{MessageID: i + 1, Chat: Chat{ID: 42}})
	}
	b.ReportAllocs()
	for b.Loop() {
		if !benchPOST(b, h, "/bot741:bench-full/sendMessage", `{"chat_id":42,"text":"Hello"}`) {
			return
		}
	}
}
