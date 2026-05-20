// Package telegym is a k6 extension that drives virtual Telegram users
// against a telegym-mock instance.
//
// Build a custom k6 binary with:
//
//	xk6 build --with github.com/kolomiichenko/telegym/pkg/xk6=./pkg/xk6
//
// Then in a scenario:
//
//	import tg from 'k6/x/telegym';
//
//	export default function () {
//	    const u = tg.newUser();
//	    u.send('/start');
//	    u.awaitButton('age_verify', 10).click();
//	}
package telegym

import (
	"go.k6.io/k6/v2/js/modules"
)

func init() {
	modules.Register("k6/x/telegym", New())
}

// RootModule is shared across all VUs; it owns the HTTP client so all VUs
// reuse connections to telegym-mock.
type RootModule struct {
	client *Client
}

func New() *RootModule {
	return &RootModule{client: NewClient()}
}

// ModuleInstance is created per-VU per-iteration by k6.
type ModuleInstance struct {
	vu     modules.VU
	client *Client
}

func (rm *RootModule) NewModuleInstance(vu modules.VU) modules.Instance {
	return &ModuleInstance{vu: vu, client: rm.client}
}

// Exports is what the JS side sees as the default export.
func (mi *ModuleInstance) Exports() modules.Exports {
	return modules.Exports{Default: &API{vu: mi.vu, client: mi.client}}
}

// API is the JS-facing surface. Keeping it small and explicit beats reflecting
// over the whole Client.
type API struct {
	vu     modules.VU
	client *Client
}

// NewUser creates a virtual user. Each VU iteration typically calls this once.
// If chatID is 0 or omitted, a unique ID is auto-assigned per call.
func (a *API) NewUser(chatID int64) *User {
	return a.client.NewUser(chatID, a.vu)
}
