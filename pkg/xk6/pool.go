package telegym

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AppendOpts is the JS-facing options object for AppendUser. Fields auto-map
// to camelCase on the JS side via sobek's struct-field resolver.
type AppendOpts struct {
	Tags  []string          `json:"tags,omitempty"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

// userRecord is the on-disk schema (one per line in NDJSON files). Kept
// minimal and open-ended: anything bot-specific goes into Attrs/Tags.
type userRecord struct {
	ChatID    int64             `json:"chat_id"`
	Username  string            `json:"username,omitempty"`
	FirstName string            `json:"first_name,omitempty"`
	SavedAt   int64             `json:"saved_at"`
	Tags      []string          `json:"tags,omitempty"`
	Attrs     map[string]string `json:"attrs,omitempty"`
}

// fileLocks holds a mutex per absolute path so concurrent writes to the same
// file serialize, but writes to different files run in parallel. Cached for
// the lifetime of the k6 process.
var (
	fileLocksMu sync.Mutex
	fileLocks   = map[string]*sync.Mutex{}
)

func lockFor(path string) *sync.Mutex {
	fileLocksMu.Lock()
	defer fileLocksMu.Unlock()
	if l, ok := fileLocks[path]; ok {
		return l
	}
	l := &sync.Mutex{}
	fileLocks[path] = l
	return l
}

// AppendUser writes one JSON-encoded user record as a newline-terminated line
// at the end of path. Creates parent directories and the file as needed.
// Safe to call concurrently from many VUs.
func (a *API) AppendUser(path string, u *User, opts AppendOpts) (err error) {
	if u == nil {
		return fmt.Errorf("appendUser: user is nil")
	}
	if path == "" {
		return fmt.Errorf("appendUser: empty path")
	}

	rec := userRecord{
		ChatID:    u.ChatID,
		Username:  u.Username,
		FirstName: u.FirstName,
		SavedAt:   time.Now().Unix(),
		Tags:      opts.Tags,
		Attrs:     opts.Attrs,
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("appendUser: marshal: %w", err)
	}
	line = append(line, '\n')

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path // fall back to whatever was passed
	}
	if dir := filepath.Dir(abs); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("appendUser: mkdir %s: %w", dir, err)
		}
	}

	l := lockFor(abs)
	l.Lock()
	defer l.Unlock()

	f, err := os.OpenFile(abs, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("appendUser: open %s: %w", abs, err)
	}
	// Promote Close errors to the returned error - writes can fail at close
	// time if the OS hasn't yet flushed buffers to disk.
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("appendUser: close %s: %w", abs, cerr)
		}
	}()
	if _, werr := f.Write(line); werr != nil {
		return fmt.Errorf("appendUser: write: %w", werr)
	}
	return nil
}
