package main

import (
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kolomiichenko/telegym/pkg/mock"
)

func TestEnvHelpers(t *testing.T) {
	t.Setenv("TG_STR", "")
	if got := envOr("TG_STR", "def"); got != "" {
		t.Errorf("envOr with empty value = %q, want empty (set wins)", got)
	}
	if got := envOr("TG_UNSET_STR", "def"); got != "def" {
		t.Errorf("envOr unset = %q, want def", got)
	}

	for v, want := range map[string]bool{"1": true, "true": true, "TRUE": true, "yes": false, "0": false} {
		t.Setenv("TG_BOOL", v)
		if got := envBool("TG_BOOL", !want); got != want {
			t.Errorf("envBool(%q) = %t, want %t", v, got, want)
		}
	}
	if !envBool("TG_UNSET_BOOL", true) {
		t.Error("envBool unset ignored fallback")
	}

	t.Setenv("TG_INT", "42")
	t.Setenv("TG_BAD", "4x")
	t.Setenv("TG_BIG", "9000000000")
	if envInt("TG_INT", 1) != 42 || envInt("TG_BAD", 1) != 1 || envInt("TG_UNSET_INT", 7) != 7 {
		t.Error("envInt did not parse or fall back")
	}
	if envInt64("TG_BIG", 1) != 9_000_000_000 || envInt64("TG_BAD", 1) != 1 || envInt64("TG_UNSET_INT", 7) != 7 {
		t.Error("envInt64 did not parse or fall back")
	}
}

func TestParseLatency(t *testing.T) {
	ms := time.Millisecond
	tests := []struct {
		in      string
		lo, hi  time.Duration
		wantErr bool
	}{
		{"", 0, 0, false},
		{"50ms", 50 * ms, 50 * ms, false},
		{"20ms-150ms", 20 * ms, 150 * ms, false},
		{" 20ms - 1s ", 20 * ms, time.Second, false},
		{"0s-0s", 0, 0, false},
		{"fast", 0, 0, true},
		{"20ms-slow", 0, 0, true},
		{"150ms-20ms", 0, 0, true},
		{"-5ms", 0, 0, true},
	}
	for _, tt := range tests {
		lo, hi, err := parseLatency(tt.in)
		if (err != nil) != tt.wantErr || lo != tt.lo || hi != tt.hi {
			t.Errorf("parseLatency(%q) = %v, %v, %v; want %v, %v, err=%t", tt.in, lo, hi, err, tt.lo, tt.hi, tt.wantErr)
		}
	}
}

func TestParseConfigDefaults(t *testing.T) {
	cfg, done, err := parseConfig(nil, io.Discard)
	if err != nil || done {
		t.Fatalf("done=%t err=%v", done, err)
	}
	want := mock.Config{
		Listen:             ":5678",
		MetricsListen:      ":9104",
		MaxMessagesPerChat: mock.DefaultMaxMessagesPerChat,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestParseConfigFlagsOverrideEnv(t *testing.T) {
	t.Setenv("TELEGYM_MOCK_LISTEN", ":1111")
	t.Setenv("TELEGYM_MOCK_QUIET", "true")
	t.Setenv("TELEGYM_MOCK_REQUEST_JOURNAL", "false")
	t.Setenv("TELEGYM_MOCK_RATE_LIMIT_GROUP", "10/min")
	t.Setenv("TELEGYM_MOCK_FAULT_RATE", "0.25")

	cfg, done, err := parseConfig([]string{
		"-listen", ":2222",
		"-metrics-listen", "",
		"-default-webhook", "http://bot/hook",
		"-file-store-max-bytes", "1024",
		"-max-messages-per-chat", "5",
		"-rate-limits",
		"-rate-limit-private", "2/s,4",
		"-latency", "10ms-20ms",
	}, io.Discard)
	if err != nil || done {
		t.Fatalf("done=%t err=%v", done, err)
	}
	want := mock.Config{
		Listen:                ":2222",
		DefaultWebhookURL:     "http://bot/hook",
		FileStoreMaxBytes:     1024,
		MaxMessagesPerChat:    5,
		Quiet:                 true,
		DisableRequestJournal: true,
		RateLimits:            true,
		LatencyMin:            10 * time.Millisecond,
		LatencyMax:            20 * time.Millisecond,
		FaultRate:             0.25,
	}
	want.FloodLimits.Private = mock.RateLimit{Rate: 2, Burst: 4}
	want.FloodLimits.Group, _ = mock.ParseRateLimit("10/min")
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("cfg = %+v\nwant  %+v", cfg, want)
	}
}

func TestParseConfigRejectsInvalidValues(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"-rate-limit-bot", "fast"}, "invalid -rate-limit-bot"},
		{[]string{"-latency", "20ms-10ms"}, "invalid -latency"},
		{[]string{"-fault-rate", "x"}, "invalid -fault-rate"},
		{[]string{"-fault-rate", "1.5"}, "invalid -fault-rate"},
		{[]string{"-fault-rate", "-0.1"}, "invalid -fault-rate"},
	} {
		_, done, err := parseConfig(tt.args, io.Discard)
		if err == nil || !strings.Contains(err.Error(), tt.want) || done {
			t.Errorf("parseConfig(%q): done=%t err=%v, want %q", tt.args, done, err, tt.want)
		}
	}
}

func TestParseConfigPrintAndExit(t *testing.T) {
	for _, tt := range []struct {
		flag, want string
	}{
		{"-version", version + "\n"},
		{"-banner", mock.Banner},
	} {
		var out strings.Builder
		_, done, err := parseConfig([]string{tt.flag}, &out)
		if err != nil || !done || out.String() != tt.want {
			t.Errorf("%s: done=%t err=%v out=%q", tt.flag, done, err, out.String())
		}
	}
}
