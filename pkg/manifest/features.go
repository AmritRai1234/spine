package manifest

// Manifest feature gating.
//
// spine_version declares two things:
//
//  1. Format contract — the manifest grammar the file follows. The parser
//     accepts every version ≤ MaxSupportedSpineVersion and refuses anything
//     newer, so a manifest written for a future runtime fails loudly at
//     startup instead of best-effort parsing.
//
//  2. Capability tier — each engine action ships with a minimum manifest
//     version (actionMinVersion). A manifest that declares an older version
//     than an action requires gets a precise startup error naming the
//     version that unlocks it. Actions absent from the map (including Go
//     plugin actions registered via Bus.RegisterAction) are available at
//     every version.
//
// Shipping a new action therefore means: add it to actionMinVersion with the
// version that introduces it, and bump MaxSupportedSpineVersion when that
// version lands. Nothing else changes — no compatibility matrix beyond this
// single source of truth.

// MaxSupportedSpineVersion is the newest manifest schema this engine parses.
const MaxSupportedSpineVersion = 3

// actionMinVersion maps engine actions to the minimum spine_version whose
// manifest may invoke them. Version 1 is the classic tier (db.*, set/unset,
// assert, math.calc, http.post, notify.webhook, log.write, fts.search,
// emit_to, queue.publish) — only post-v1 features are listed here.
var actionMinVersion = map[string]int{
	"email.send":      2,
	"email.broadcast": 2,

	// Tier 3 — money movement: outbound Stripe API calls with a live secret.
	"stripe.checkout": 3,
}
