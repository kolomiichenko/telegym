package telegym

import (
	"os"

	"github.com/kolomiichenko/telegym/pkg/branding"
)

// init runs at module load, BEFORE k6's main() prints the Grafana banner,
// so the boot order in `./bin/k6 run scenario.js` is:
//
//	telegym mascot -> Grafana logo -> scenario summary
//
// Banner goes to stderr so JSON output (k6's `--out json`) on stdout
// stays machine-parseable.
func init() {
	branding.PrintBanner(os.Stderr)
}
