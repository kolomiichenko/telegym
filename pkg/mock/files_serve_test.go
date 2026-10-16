package mock

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDebugServeFileHeaders(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	tests := []struct {
		name            string
		declared        string
		body            []byte
		wantType        string
		wantDisposition string
	}{
		{"png", "image/png", png, "image/png", `inline; filename="f.bin"`},
		{"png declared as html", "text/html", append(png, "<script>alert(1)</script>"...), "image/png", `inline; filename="f.bin"`},
		{"html", "text/html", []byte("<html><script>alert(1)</script></html>"), "application/octet-stream", `attachment; filename="f.bin"`},
		{"svg declared as image", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`), "application/octet-stream", `attachment; filename="f.bin"`},
		{"video", "video/mp4", []byte("\x00\x00\x00\x18ftypmp42"), "video/mp4", `attachment; filename="f.bin"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(Config{Quiet: true})
			id := srv.Store().Files.Put("photo", tt.declared, "f.bin", tt.body)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/files/"+id, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d", rec.Code)
			}
			h := rec.Header()
			if got := h.Get("Content-Type"); got != tt.wantType {
				t.Errorf("Content-Type=%q, want %q", got, tt.wantType)
			}
			if got := h.Get("Content-Disposition"); got != tt.wantDisposition {
				t.Errorf("Content-Disposition=%q, want %q", got, tt.wantDisposition)
			}
			if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") == "" {
				t.Errorf("missing nosniff/CSP headers: %v", h)
			}
		})
	}
}
