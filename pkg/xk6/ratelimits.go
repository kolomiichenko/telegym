package telegym

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// SetRateLimits changes the mock's flood limits for every VU, e.g.
// {enabled: true, bot: '5/s,5'}, and returns the new settings.
func (a *API) SetRateLimits(limits map[string]any) (map[string]any, error) {
	body, err := json.Marshal(limits)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPut, a.client.mockURL+"/debug/ratelimits", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		refusal, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ratelimits: %d %s", resp.StatusCode, refusal)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
