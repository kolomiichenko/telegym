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

// WebhookDispatcher delivers Updates to a bot's registered webhook URL. Used by the
// debug inject endpoints and by the xk6 extension to drive virtual users.
type WebhookDispatcher struct {
	client      *http.Client
	defaultURL  string // fallback if a bot never called setWebhook
	logFailures bool
	metrics     *Metrics // optional - instruments dispatch counts and duration
}

func NewWebhookDispatcher(defaultURL string) *WebhookDispatcher {
	return &WebhookDispatcher{
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

// Deliver posts the update to the bot's webhook. Returns the bot's HTTP
// status and any error so callers can surface delivery problems to the test
// runner instead of swallowing them.
func (d *WebhookDispatcher) Deliver(ctx context.Context, bot *botEntry, upd Update) (int, error) {
	url, secret := bot.Webhook()
	if url == "" {
		url = d.defaultURL
	}
	if url == "" {
		d.observe("no_url", 0)
		return 0, fmt.Errorf("no webhook URL set for bot (call setWebhook or set TELEGYM_MOCK_DEFAULT_WEBHOOK)")
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

func (d *WebhookDispatcher) observe(result string, dur time.Duration) {
	if d.metrics == nil {
		return
	}
	d.metrics.WebhookDispatchTotal.WithLabelValues(result).Inc()
	d.metrics.WebhookDispatchDuration.Observe(dur.Seconds())
}
