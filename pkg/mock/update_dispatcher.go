package mock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// UpdateDispatcher delivers Updates to a bot's webhook, or queues them for
// polling when no webhook is configured.
type UpdateDispatcher struct {
	client      *http.Client
	defaultURL  string // fallback if a bot never called setWebhook
	logFailures bool
	metrics     *Metrics // optional - instruments dispatch counts and duration
}

func NewUpdateDispatcher(defaultURL string) *UpdateDispatcher {
	return &UpdateDispatcher{
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        500,
				MaxIdleConnsPerHost: 500,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		defaultURL:  defaultURL,
		logFailures: true,
	}
}

func (d *UpdateDispatcher) target(bot *botEntry) (url, secret string) {
	bot.msgMu.RLock()
	defer bot.msgMu.RUnlock()
	url, secret = bot.webhookURL, bot.secretToken
	// An explicit webhook deletion disables the configured default as well.
	if !bot.webhookSet {
		url = d.defaultURL
	}
	return url, secret
}

func (d *UpdateDispatcher) hasTarget(bot *botEntry) bool {
	url, _ := d.target(bot)
	return url != ""
}

// Deliver posts the update to the bot's webhook and returns its HTTP status,
// or queues it for getUpdates and returns zero.
func (d *UpdateDispatcher) Deliver(ctx context.Context, bot *botEntry, upd Update) (int, error) {
	url, secret := d.target(bot)
	if url == "" {
		bot.updates.Enqueue(upd)
		return 0, nil
	}

	body, err := json.Marshal(upd)
	if err != nil {
		return 0, fmt.Errorf("marshal update: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		// Real Telegram sends this header so bots can verify the call originated
		// from their setWebhook setup; mirror that behavior.
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	}

	start := time.Now()
	resp, err := d.client.Do(req)
	dur := time.Since(start)
	if err != nil {
		d.observe("fail", dur)
		if d.logFailures {
			log.Printf("dispatcher: deliver to %s failed: %v", url, err)
		}
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		d.observe("fail", dur)
		if d.logFailures {
			log.Printf("dispatcher: %s returned %d for update_id=%d", url, resp.StatusCode, upd.UpdateID)
		}
	} else {
		d.observe("ok", dur)
	}
	return resp.StatusCode, nil
}

func (d *UpdateDispatcher) observe(result string, dur time.Duration) {
	if d.metrics == nil {
		return
	}
	d.metrics.WebhookDispatchTotal.WithLabelValues(result).Inc()
	d.metrics.WebhookDispatchDuration.Observe(dur.Seconds())
}
