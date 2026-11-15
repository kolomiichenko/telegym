package mock

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeliverToWebhook(t *testing.T) {
	var gotSecret string
	status := http.StatusOK
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSecret = r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
		w.WriteHeader(status)
	}))
	defer receiver.Close()

	store := NewStore()
	bot := store.Bot("950:deliver")
	bot.SetWebhook(receiver.URL, "s3cret")
	d := NewWebhookDispatcher("")
	d.logFailures = false

	code, err := d.Deliver(context.Background(), bot, Update{})
	if err != nil || code != http.StatusOK {
		t.Fatalf("Deliver = %d, %v; want 200", code, err)
	}
	if gotSecret != "s3cret" {
		t.Fatalf("secret header = %q, want s3cret", gotSecret)
	}

	// A failing webhook reports its status without an error.
	status = http.StatusInternalServerError
	if code, err := d.Deliver(context.Background(), bot, Update{}); err != nil || code != http.StatusInternalServerError {
		t.Fatalf("Deliver = %d, %v; want 500", code, err)
	}

	// An unreachable webhook is an error.
	receiver.Close()
	if _, err := d.Deliver(context.Background(), bot, Update{}); err == nil {
		t.Fatal("Deliver to a closed server succeeded")
	}
}

func TestDeliverDefaultURLAndNoTarget(t *testing.T) {
	hits := 0
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++ }))
	defer receiver.Close()

	store := NewStore()
	d := NewWebhookDispatcher(receiver.URL)
	res, err := d.DeliverUpdate(context.Background(), store.Bot("951:default"), Update{})
	if err != nil || res.Status != http.StatusOK || res.UpdateID == 0 || hits != 1 {
		t.Fatalf("default webhook: res=%+v err=%v hits=%d", res, err, hits)
	}

	// An explicit deleteWebhook disables the default too; without polling
	// there is nowhere to deliver.
	bot := store.Bot("952:none")
	bot.SetWebhook("", "")
	if _, err := d.Deliver(context.Background(), bot, Update{}); !errors.Is(err, ErrNoUpdateTarget) {
		t.Fatalf("Deliver err = %v, want ErrNoUpdateTarget", err)
	}

	// Once the bot polls, the update is queued instead.
	bot.updates.OptIn()
	res, err = d.DeliverUpdate(context.Background(), bot, Update{})
	if err != nil || !res.Queued || res.Status != 0 || bot.updates.Len() != 1 {
		t.Fatalf("queued: res=%+v err=%v len=%d", res, err, bot.updates.Len())
	}
}
