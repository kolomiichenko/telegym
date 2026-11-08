package telegym

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPISetRateLimits(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/debug/ratelimits" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("bad JSON body: %v", err)
		}
		if sent["bot"] == "nope" {
			http.Error(w, "bad limit", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(sent)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("TELEGYM_MOCK_URL", srv.URL)
	api := &API{client: NewClient()}

	got, err := api.SetRateLimits(map[string]any{"enabled": true, "bot": "5/s,5"})
	if err != nil {
		t.Fatalf("SetRateLimits: %v", err)
	}
	if got["bot"] != "5/s,5" || sent["enabled"] != true {
		t.Errorf("response %+v, sent %+v", got, sent)
	}

	if _, err := api.SetRateLimits(map[string]any{"bot": "nope"}); err == nil || !strings.Contains(err.Error(), "bad limit") {
		t.Errorf("err = %v, want the mock's refusal", err)
	}
}
