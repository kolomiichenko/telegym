// Command telegym-mock runs the telegym mock Bot API server.
//
// Configuration is via flags or environment variables:
//
//	-listen,           TELEGYM_MOCK_LISTEN              ":5678"
//	-metrics-listen,   TELEGYM_MOCK_METRICS_LISTEN      ":9104"  (empty=disabled)
//	-default-webhook,  TELEGYM_MOCK_DEFAULT_WEBHOOK     ""  (use per-bot setWebhook)
//	-quiet,            TELEGYM_MOCK_QUIET               false
//	-banner                                             print ASCII banner with ANSI colors and exit
package main

import (
	"flag"
	"io"
	"log"
	"os"

	"github.com/kolomiichenko/telegym/pkg/mock"
)

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		return v == "1" || v == "true" || v == "TRUE"
	}
	return fallback
}

func main() {
	cfg := mock.Config{}

	var bannerOnly bool
	flag.StringVar(&cfg.Listen, "listen", envOr("TELEGYM_MOCK_LISTEN", ":5678"), "HTTP listen address")
	flag.StringVar(&cfg.MetricsListen, "metrics-listen", envOr("TELEGYM_MOCK_METRICS_LISTEN", ":9104"), "Prometheus metrics listen address (empty=disabled)")
	flag.StringVar(&cfg.DefaultWebhookURL, "default-webhook", envOr("TELEGYM_MOCK_DEFAULT_WEBHOOK", ""), "Fallback webhook URL when a bot has not called setWebhook")
	flag.BoolVar(&cfg.Quiet, "quiet", envBool("TELEGYM_MOCK_QUIET", false), "Suppress per-request logs")
	flag.BoolVar(&bannerOnly, "banner", false, "Print the ASCII banner with raw ANSI colors to stdout and exit (for `freeze` and similar renderers)")
	flag.Parse()

	if bannerOnly {
		// Write raw (always-colored) banner so downstream tools like
		// `freeze` can render the ANSI escapes into SVG/PNG. We
		// bypass mock.PrintBanner because that one strips colors
		// when stdout is a pipe.
		_, _ = io.WriteString(os.Stdout, mock.Banner)
		return
	}

	srv := mock.New(cfg)
	if err := srv.Run(); err != nil {
		log.Fatalf("telegym-mock: %v", err)
	}
}
