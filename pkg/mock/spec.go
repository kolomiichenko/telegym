package mock

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

// Bot API spec sourced from https://github.com/PaulSonOfLars/telegram-bot-api-spec
// Refresh with `make refresh-spec` (or `curl ... > pkg/mock/spec/api.json`).
//
//go:embed spec/api.json
var apiSpecRaw []byte

// APISpec is the loaded shape of api.json. Only the fields the dispatcher
// needs (methods + types + their fields) are decoded; descriptions and href
// are ignored.
type APISpec struct {
	Version     string                `json:"version"`
	ReleaseDate string                `json:"release_date"`
	Methods     map[string]SpecMethod `json:"methods"`
	Types       map[string]SpecType   `json:"types"`
}

type SpecMethod struct {
	Name    string      `json:"name"`
	Returns []string    `json:"returns"`
	Fields  []SpecField `json:"fields"`
}

type SpecType struct {
	Name     string      `json:"name"`
	Fields   []SpecField `json:"fields"`
	Subtypes []string    `json:"subtypes,omitempty"` // for abstract types like InputMedia
}

type SpecField struct {
	Name     string   `json:"name"`
	Types    []string `json:"types"`
	Required bool     `json:"required"`
}

var (
	specOnce sync.Once
	specVal  *APISpec
	specErr  error
)

// LoadAPISpec parses the embedded api.json once and caches the result.
// Returns an error only on the first call if the embed is corrupt; subsequent
// calls return the cached value.
func LoadAPISpec() (*APISpec, error) {
	specOnce.Do(func() {
		var s APISpec
		if err := json.Unmarshal(apiSpecRaw, &s); err != nil {
			specErr = fmt.Errorf("parse embedded api.json: %w", err)
			return
		}
		specVal = &s
	})
	return specVal, specErr
}

// MustLoadAPISpec panics on failure. Use this at process startup where a
// corrupt spec is unrecoverable.
func MustLoadAPISpec() *APISpec {
	s, err := LoadAPISpec()
	if err != nil {
		panic(err)
	}
	return s
}
