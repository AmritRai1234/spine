package manifest

import (
	"fmt"
	"sort"
	"strings"
)

// actionSchemas maps every built-in action to the set of step-level config
// keys it reads from Config (custom `key: value` pairs), plus its known
// aliases. This is the whitelist behind "unknown option" validation: a step
// invoking a built-in action may only use these keys — anything else is a
// typo (e.g. `keyy:` under db.upsert, which was silently accepted and
// ignored before 2026-09-01, leaving the real `key:` unset and the upsert
// falling back to its `id` default).
//
// Keys handled STRUCTURALLY by the parser (and therefore valid on every
// step) are NOT listed here: action, if, table, input, url, message, where,
// on_failure, on_error, compensate, max_attempts, backoff_ms, key (parser
// intercepts for db.upsert), and key_list (internal marker).
//
// Custom actions registered via Bus.RegisterAction are exempt: the manifest
// package cannot know their config vocabulary, so steps whose action is not
// in actionSchemas skip config-key validation entirely (their Config remains
// free-form by design).
var actionSchemas = map[string]map[string]bool{
	"db.insert":            keys("sync"),
	"db.update":            keys(),
	"db.upsert":            keys("conflict_key", "key"),
	"db.delete":            keys(),
	"db.sum":               keys("as", "column"),
	"db.lookup":            keys("as", "key_column", "optional", "value_expr"),
	"db.adjust":            keys("by", "column", "floor"),
	"set":                  keys(), // free-form: every pair is a payload field
	"unset":                keys("fields"),
	"assert":               keys("condition", "message"),
	"log.write":            keys(), // uses step.Message
	"http.post":            keys(), // uses step.URL / step.Input
	"notify.webhook":       keys(), // uses step.URL
	"fts.search":           keys("query"),
	"emit_to":              keys("stream"),
	"queue.publish":        keys(),
	"emit":                 keys(),
	"math.calc":            keys("expr", "set"),
	"email.send":           keys("body", "from", "html", "subject", "to", "unsubscribe_url"),
	"email.broadcast":      keys("body", "email_column", "from", "html", "subject", "unsubscribe_url"),
	"subscriptions.sweep":  keys(),
	"db.fanout":            keys("batch_size", "due_column", "emit_event", "interval_column"),
	"slots.generate":       keys("capacity", "close", "days_ahead", "duration_minutes", "open", "weekdays"),
	"stripe.checkout":      keys("amount", "cancel_url", "currency", "customer_email", "description", "order_id", "success_url"),
	"stripe.connect":       keys("mode"),
	"domain.connect":       keys("mode"),
	"social.connect":       keys("mode"),
	"social.post":          keys("account_key", "platform", "text"),
	"notify.push":          keys("body", "title"),
	"notify.push.register": keys("platform_column", "table", "token_column", "user_column"),
	"auth.hash":            keys("password", "set"),
	"auth.verify":          keys("hash", "password", "set"),
	"tracking.register":    keys("as", "body", "from", "headers", "numbers", "optional", "url"),
}

// builtinActions is the sorted list of every action the engine dispatches
// natively (mirrors actions.go's switch). ValidateSchema rejects unknown
// action names with a did-you-mean hint; Go plugin actions registered via
// Bus.RegisterAction are validated separately (the manifest package cannot
// see them, so engine.NewFromFile re-checks step actions against
// customActions at construction).
var builtinActions = func() []string {
	names := make([]string, 0, len(actionSchemas))
	for name := range actionSchemas {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}()

func keys(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// isBuiltinAction reports whether action is natively dispatched by the engine.
func isBuiltinAction(action string) bool {
	_, ok := actionSchemas[action]
	return ok
}

// validateStepConfig checks a step's Config keys against its action's
// whitelist. Unknown keys are a parse error with a did-you-mean suggestion —
// a typo'd key (keyy: instead of key:) was previously accepted into Config
// and silently ignored, leaving the real option unset.
func validateStepConfig(routeOn string, stepIndex int, step RouteStep) error {
	schema, builtin := actionSchemas[step.Action]
	if !builtin {
		// Custom/plugin action: config vocabulary is unknown here — skip.
		return nil
	}
	if step.Action == "set" {
		// set is free-form BY DESIGN: every key:value pair becomes a payload
		// field, so any key is valid.
		return nil
	}
	// Sorted key iteration for deterministic error ordering.
	names := make([]string, 0, len(step.Config))
	for k := range step.Config {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if schema[k] || k == "key_list" { // key_list is the parser's internal list-form marker
			continue
		}
		// `key` on db.upsert is parser-intercepted (scalar or list); it never
		// lands in Config as "key" — but the AUTHOR wrote it, so if a typo'd
		// variant reaches here (it can't for known keys) we still hint well
		// via suggestion against the schema + the intercepted keys.
		hintable := make([]string, 0, len(schema)+4)
		for kk := range schema {
			hintable = append(hintable, kk)
		}
		hintable = append(hintable, "key", "sync", "if", "where", "table")
		suggestion := suggestSimilarKey(k, hintable)
		if suggestion != "" {
			return fmt.Errorf("route '%s', step %d (action '%s'): unknown option '%s'. Did you mean '%s'?",
				routeOn, stepIndex+1, step.Action, k, suggestion)
		}
		return fmt.Errorf("route '%s', step %d (action '%s'): unknown option '%s' (valid options: %s)",
			routeOn, stepIndex+1, step.Action, k, strings.Join(sortedKeys(schema), ", "))
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// validateActionName checks a step's action against the built-in dispatch
// table. A strong typo of a builtin (did-you-mean match) is a parse error —
// it would otherwise dispatch to the engine's default case and silently
// no-op. A name with no builtin resemblance is left to the engine: it may
// be a Go plugin action registered via Bus.RegisterAction (which can happen
// before or after manifest parsing), and dispatchAction now fails loudly at
// runtime if nothing is registered under that name.
func validateActionName(routeOn string, stepIndex int, action string) error {
	if isBuiltinAction(action) {
		return nil
	}
	suggestion := suggestSimilarKey(action, builtinActions)
	if suggestion != "" {
		return fmt.Errorf("route '%s', step %d: unknown action '%s'. Did you mean '%s'?", routeOn, stepIndex+1, action, suggestion)
	}
	return nil
}
