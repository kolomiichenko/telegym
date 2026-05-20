package mock

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// GenericDispatch is the catch-all handler for Bot API methods that don't have
// an explicit implementation. It looks the method up in the embedded spec
// and returns a zero-value response of the declared return type so any Bot
// API client can unmarshal it without error.
//
// GenericDispatch does NOT touch the store: side effects (storing outbound
// messages, applying webhook URL) only happen for methods with explicit
// handlers in handlers.go. forwardMessage, for example, won't show up in
// /debug/messages - the load-test loop runs through the explicit handlers.
//
// For union return types like `Message OR Boolean` it always picks the
// first variant. Add an explicit handler if that breaks a specific client.
func (h *Handlers) GenericDispatch(c *gin.Context, bot *botEntry, methodName string) {
	spec, err := LoadAPISpec()
	if err != nil || spec == nil {
		// Spec broken at startup - fall back to generic true.
		c.JSON(http.StatusOK, APIResponse{OK: true, Result: true})
		return
	}
	method, ok := spec.Methods[methodName]
	if !ok || len(method.Returns) == 0 {
		c.JSON(http.StatusOK, APIResponse{OK: true, Result: true})
		return
	}

	result := buildZeroValue(spec, method.Returns[0], 0)

	// Cosmetic: for the common Message return, populate a few fields with
	// realistic values so logs are readable and clients that introspect
	// {message_id, chat.id, from.id} see plausible data.
	if method.Returns[0] == "Message" {
		if m, ok := result.(map[string]any); ok {
			body, _ := readBody(c)
			if chatID := toInt64(body["chat_id"]); chatID != 0 {
				m["chat"] = map[string]any{"id": chatID, "type": "private"}
			}
			m["message_id"] = h.Store.NextMessageID()
			m["date"] = time.Now().Unix()
			m["from"] = bot.identity
		}
	}

	c.JSON(http.StatusOK, APIResponse{OK: true, Result: result})
}

const maxBuildDepth = 4 // recursion limit for nested type construction

// buildZeroValue returns a JSON-serializable zero value of a Bot API type.
// `typeName` may be a primitive ("Integer", "String", "Boolean"), an
// "Array of X" pseudo-type, or the name of a struct defined in the spec.
// Optional fields are omitted; required fields are emitted with recursively-
// constructed zero values.
func buildZeroValue(spec *APISpec, typeName string, depth int) any {
	if depth >= maxBuildDepth {
		return nil
	}
	switch typeName {
	case "Boolean", "True":
		// Most Bot API operations return `true` on success; "False" doesn't
		// appear as a return type in the spec, so true is always safe.
		return true
	case "Integer":
		return 0
	case "Float", "Float number":
		return 0.0
	case "String":
		return ""
	case "InputFile":
		// InputFile is for inputs only; never appears as a response field.
		return nil
	case "CallbackGame":
		// Documented as "Currently holds no information" - empty object is right.
		return map[string]any{}
	}
	if rest := strings.TrimPrefix(typeName, "Array of "); rest != typeName {
		// Empty arrays are valid for any Array of X return.
		return []any{}
	}

	t, ok := spec.Types[typeName]
	if !ok {
		// Unknown / abstract type - return empty object as a safe fallback.
		return map[string]any{}
	}

	// Abstract types (e.g. InputMedia, BotCommandScope) carry subtypes
	// instead of fields; pick the first subtype for a usable zero value.
	if len(t.Fields) == 0 && len(t.Subtypes) > 0 {
		return buildZeroValue(spec, t.Subtypes[0], depth+1)
	}

	out := make(map[string]any, len(t.Fields))
	for _, f := range t.Fields {
		if !f.Required {
			continue
		}
		// Field type is a union of candidates; pick the first.
		if len(f.Types) == 0 {
			continue
		}
		out[f.Name] = buildZeroValue(spec, f.Types[0], depth+1)
	}
	return out
}
