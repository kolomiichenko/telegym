package mock

import (
	"encoding/json"
	"testing"
)

// TestAllSpecMethodsBuildable ensures every method in the embedded Bot API
// spec produces a JSON-marshalable response through the dispatcher's
// zero-value builder. Run after `make refresh-spec` to catch breakage
// introduced by upstream changes to the spec (new abstract types, removed
// referents, malformed entries) before they reach load tests.
func TestAllSpecMethodsBuildable(t *testing.T) {
	spec, err := LoadAPISpec()
	if err != nil {
		t.Fatalf("LoadAPISpec: %v", err)
	}
	if len(spec.Methods) == 0 {
		t.Fatal("spec has no methods")
	}

	for name, m := range spec.Methods {
		if len(m.Returns) == 0 {
			continue
		}
		result := buildZeroValue(spec, m.Returns[0], 0)
		if _, err := json.Marshal(APIResponse{OK: true, Result: result}); err != nil {
			t.Errorf("method %s (returns %v): marshal failed: %v", name, m.Returns, err)
		}
	}
}

// TestSpecMetadata is a sanity check that the embedded spec parsed and
// contains the expected top-level fields. Mainly catches accidental
// truncation or wrong file replacement after `make refresh-spec`.
func TestSpecMetadata(t *testing.T) {
	spec, err := LoadAPISpec()
	if err != nil {
		t.Fatalf("LoadAPISpec: %v", err)
	}
	if spec.Version == "" {
		t.Error("spec.Version is empty")
	}
	if len(spec.Methods) < 100 {
		t.Errorf("spec.Methods has only %d entries (expected >100)", len(spec.Methods))
	}
	if len(spec.Types) < 100 {
		t.Errorf("spec.Types has only %d entries (expected >100)", len(spec.Types))
	}
	// A few canonical methods that have been in the API for years; their
	// absence means the spec file is wrong or we read the wrong key.
	for _, want := range []string{"sendMessage", "getMe", "setWebhook", "editMessageText"} {
		if _, ok := spec.Methods[want]; !ok {
			t.Errorf("expected method %q missing from spec", want)
		}
	}
}
