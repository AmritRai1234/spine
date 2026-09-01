package manifest

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type parseState int

const (
	sTop parseState = iota
	sIncludes
	sDatabase
	sDbTables
	sDbOutbox
	sAccess
	sAccessEntry
	sAccessEvents
	sNodes
	sNodeBody
	sNodeOwnFiles
	sNodeEmits
	sNodeEmitEntry
	sNodeEmitPayload
	sNodeListens
	sNodeListenEntry
	sNodeListenPayload
	sRoutes
	sRouteBody
	sRouteSteps
	sRouteStepBody
)

// parseError formats an error with file path and line number context.
func parseError(file string, lineno int, format string, args ...interface{}) error {
	prefix := filepath.Base(file)
	if lineno > 0 {
		prefix = fmt.Sprintf("%s:%d", prefix, lineno)
	}
	return fmt.Errorf("%s: %s", prefix, fmt.Sprintf(format, args...))
}

// getIndent computes indentation level from leading whitespace.
// Tabs are normalized to 2 spaces each for tolerance.
func getIndent(line string) int {
	count := 0
	for _, c := range line {
		if c == ' ' {
			count++
		} else if c == '\t' {
			count += 2
		} else {
			break
		}
	}
	return count / 2
}

// hasMixedWhitespace returns true if a line has both leading tabs and spaces.
func hasMixedWhitespace(line string) bool {
	hasTabs := false
	hasSpaces := false
	for _, c := range line {
		if c == '\t' {
			hasTabs = true
		} else if c == ' ' {
			hasSpaces = true
		} else {
			break
		}
	}
	return hasTabs && hasSpaces
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// parseBoolFlag parses a manifest boolean value fail-closed: strconv.ParseBool
// accepts true/false and their case variants (True, TRUE, 1, t, ...), and
// anything else is a hard parse error. The previous exact-match `v == "true"`
// silently treated `True`, `TRUE`, `yes`, or `1` as FALSE — e.g. a
// `read_only: True` role became writable with no warning.
func parseBoolFlag(file string, lineno int, key, v string) (bool, error) {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return false, parseError(file, lineno, "invalid boolean value '%s' for '%s' (expected true or false)", v, key)
	}
	return b, nil
}

// fieldTypeValue normalizes a payload field type declaration: strips an
// inline ` # comment` and unquotes. Previously `email: string # primary`
// stored "string # primary" as the type, which mapped to `any` in codegen
// and was silently NEVER type-checked at runtime — a comment disabled the
// contract end-to-end.
func fieldTypeValue(v string) string {
	if c := strings.Index(v, " #"); c >= 0 {
		v = v[:c]
	}
	return unquote(strings.TrimSpace(v))
}

func kvValue(trimmed, key string) (string, bool) {
	if strings.HasPrefix(trimmed, key) && len(trimmed) > len(key) && trimmed[len(key)] == ':' {
		return strings.TrimSpace(trimmed[len(key)+1:]), true
	}
	return "", false
}

func isListItem(trimmed string) bool {
	return strings.HasPrefix(trimmed, "- ")
}

func listKvValue(trimmed, key string) (string, bool) {
	if !isListItem(trimmed) {
		return "", false
	}
	return kvValue(trimmed[2:], key)
}

// ParseManifest reads a .spine manifest file and returns the parsed schema.
// This is the public API entry point — it delegates to the internal parser
// with an empty include stack for circular include detection, then runs
// semantic validation ONCE on the fully merged schema (root + all includes),
// so routes may reference events declared in included files.
func ParseManifest(manifestPath string) (*SpineSchema, error) {
	absPath, err := filepath.Abs(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve path '%s': %w", manifestPath, err)
	}
	schema, err := parseManifestWithStack(absPath, nil)
	if err != nil {
		return nil, err
	}
	// Post-merge validation: run against the merged schema so cross-file
	// references (root route → event declared in an included file) validate.
	if err := ValidateSchema(absPath, schema); err != nil {
		return nil, err
	}
	return schema, nil
}

// parseManifestWithStack is the internal parser that carries the include chain
// for circular dependency detection.
func parseManifestWithStack(manifestPath string, includeStack []string) (*SpineSchema, error) {
	// Circular include detection
	for _, ancestor := range includeStack {
		if ancestor == manifestPath {
			chain := append(includeStack, manifestPath)
			names := make([]string, len(chain))
			for i, p := range chain {
				names[i] = filepath.Base(p)
			}
			return nil, fmt.Errorf("circular include detected: %s", strings.Join(names, " → "))
		}
	}

	f, err := os.Open(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("cannot open manifest '%s': %w", manifestPath, err)
	}
	defer f.Close()

	schema := &SpineSchema{}
	state := sTop

	var curNode *Node

	var curEmit *Emit
	var curListen *Listen
	var curRoute *Route
	var curStep *RouteStep
	var curAccess *AccessRule
	// eff := indent - nodeShift normalizes NODES-section depth checks. List-
	// form nodes ("- name: X") occupy the SAME indentation levels as map-form
	// bodies (node at level 1, emits:/listens: at level 2, entries at level 3,
	// payload fields at level 5), so nodeShift stays 0; only node creation
	// differs (dash-list vs bare key).
	nodeShift := 0

	// Duplicate tracking
	seenNodes := make(map[string]int)   // node name → first line number
	seenTables := make(map[string]bool) // table name deduplication
	seenRoles := make(map[string]int)   // role name → first line number

	scanner := bufio.NewScanner(f)
	lineno := 0

	for scanner.Scan() {
		lineno++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		indent := getIndent(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Warn on mixed tabs + spaces (parse continues, but flag it)
		if hasMixedWhitespace(line) {
			return nil, parseError(manifestPath, lineno, "mixed tabs and spaces in indentation; use spaces only")
		}

		// ===== TOP LEVEL =====
		if indent == 0 {
			state = sTop
			curNode = nil
			curEmit = nil
			curListen = nil
			curRoute = nil
			curStep = nil

			if v, ok := kvValue(trimmed, "spine_version"); ok {
				fmt.Sscanf(v, "%d", &schema.SpineVersion)
				continue
			}
			if v, ok := kvValue(trimmed, "include"); ok {
				schema.Includes = append(schema.Includes, unquote(v))
				continue
			}
			if trimmed == "includes:" || trimmed == "include:" {
				state = sIncludes
				continue
			}
			if trimmed == "database:" {
				state = sDatabase
				continue
			}
			if trimmed == "nodes:" {
				state = sNodes
				continue
			}
			if trimmed == "routes:" {
				state = sRoutes
				continue
			}
			if trimmed == "access:" {
				state = sAccess
				continue
			}

			if v, ok := kvValue(trimmed, "tenant"); ok {
				schema.Tenant = unquote(v)
				continue
			}

			// Unknown top-level key detection with "did you mean?" suggestion
			if strings.Contains(trimmed, ":") {
				key := strings.TrimSpace(trimmed[:strings.Index(trimmed, ":")])
				validKeys := []string{"spine_version", "tenant", "includes", "database", "access", "nodes", "routes"}
				suggestion := suggestSimilarKey(key, validKeys)
				if suggestion != "" {
					return nil, parseError(manifestPath, lineno, "unknown top-level key '%s'. Did you mean '%s'?", key, suggestion)
				}
				return nil, parseError(manifestPath, lineno, "unknown top-level key '%s' (expected: spine_version, includes, database, access, nodes, routes)", key)
			}
		}

		// ===== INCLUDES =====
		if state == sIncludes {
			if indent >= 1 && isListItem(trimmed) {
				schema.Includes = append(schema.Includes, unquote(trimmed[2:]))
				continue
			}
		}

		// ===== DATABASE =====
		if state == sDatabase || state == sDbTables || state == sDbOutbox {
			if indent == 1 {
				if trimmed == "tables:" {
					state = sDbTables
					continue
				}
				if trimmed == "outbox:" {
					state = sDbOutbox
					continue
				}
			}
		}

		if state == sDbTables {
			if indent == 2 && isListItem(trimmed) {
				tableName := unquote(trimmed[2:])
				if !seenTables[tableName] {
					schema.DbTables = append(schema.DbTables, tableName)
					schema.Database.Tables = append(schema.Database.Tables, tableName)
					seenTables[tableName] = true
				}
				continue
			}
			if indent <= 1 {
				state = sTop
			}
		}

		if state == sDbOutbox {
			if indent == 2 {
				if v, ok := kvValue(trimmed, "max_workers"); ok {
					if n, err := strconv.Atoi(v); err == nil {
						schema.Database.Outbox.MaxWorkers = n
					}
					continue
				}
				if v, ok := kvValue(trimmed, "max_retries"); ok {
					if n, err := strconv.Atoi(v); err == nil {
						schema.Database.Outbox.MaxRetries = n
					}
					continue
				}
				if v, ok := kvValue(trimmed, "backoff_ms"); ok {
					if n, err := strconv.Atoi(v); err == nil {
						schema.Database.Outbox.BackoffMs = n
					}
					continue
				}
			}
			if indent <= 1 {
				state = sTop
			}
		}

		// ===== ACCESS =====
		if state >= sAccess && state <= sAccessEvents {
			// New access entry: "- role: <name>"
			if indent == 1 {
				if v, ok := listKvValue(trimmed, "role"); ok {
					roleName := unquote(v)
					if roleName == "" {
						return nil, parseError(manifestPath, lineno, "access role name cannot be empty")
					}
					if prevLine, exists := seenRoles[roleName]; exists {
						return nil, parseError(manifestPath, lineno, "duplicate access role '%s' (first defined on line %d)", roleName, prevLine)
					}
					seenRoles[roleName] = lineno
					schema.Access = append(schema.Access, AccessRule{Role: roleName})
					curAccess = &schema.Access[len(schema.Access)-1]
					state = sAccessEntry
					continue
				}
			}

			// Access entry fields (indent=2)
			if state == sAccessEntry || state == sAccessEvents {
				if indent == 2 && curAccess != nil {
					if v, ok := kvValue(trimmed, "key"); ok {
						keyVal := unquote(v)
						// Expand $ENV_VAR references — accepts both "$VAR" and "$env.VAR"
						if strings.HasPrefix(keyVal, "$") {
							envName := strings.TrimPrefix(keyVal[1:], "env.")
							keyVal = os.Getenv(envName)
						}
						// Fail closed: a role whose key resolved to empty is an open
						// door (every unauthenticated caller matches it). Refuse to
						// start instead of shipping an exposed admin/staff role.
						if keyVal == "" {
							return nil, parseError(manifestPath, lineno, "access role '%s' has an empty key — refusing to start with an open role (set the key directly or via its environment variable)", curAccess.Role)
						}
						curAccess.Key = keyVal
						state = sAccessEntry
						continue
					}
					if v, ok := kvValue(trimmed, "tenant"); ok {
						// Single-tenant engine: `tenant:` under an access role
						// is inert metadata. Accept it (backward compat) but
						// do not store a field that implies row isolation.
						_ = unquote(v)
						state = sAccessEntry
						continue
					}
					if v, ok := kvValue(trimmed, "read_only"); ok {
						readOnly, berr := parseBoolFlag(manifestPath, lineno, "read_only", v)
						if berr != nil {
							return nil, berr
						}
						curAccess.ReadOnly = readOnly
						state = sAccessEntry
						continue
					}
					if v, ok := kvValue(trimmed, "filter"); ok {
						curAccess.Filter = unquote(v)
						state = sAccessEntry
						continue
					}
					if trimmed == "events:" {
						state = sAccessEvents
						continue
					}
				}
			}

			// Access events whitelist (indent=3)
			if state == sAccessEvents {
				if indent == 3 && isListItem(trimmed) && curAccess != nil {
					curAccess.Events = append(curAccess.Events, unquote(trimmed[2:]))
					continue
				}
				if indent <= 2 {
					state = sAccessEntry
					// Fall through to re-evaluate
				}
			}

			if indent == 0 {
				state = sTop
				curAccess = nil
			}
		}

		// ===== NODES =====
		if state >= sNodes && state <= sNodeListenPayload {
			if v, ok := listKvValue(trimmed, "name"); ok && indent == 1 {
				// List-form node: "- name: X" — same node semantics as the
				// map form, with the body indented 2 levels deeper. Accepted
				// explicitly so payload contracts and route validation are
				// NEVER silently skipped (see nodeShift below).
				nodeName := unquote(v)
				if firstLine, exists := seenNodes[nodeName]; exists {
					return nil, parseError(manifestPath, lineno, "duplicate node name '%s' (first declared at line %d)", nodeName, firstLine)
				}
				seenNodes[nodeName] = lineno
				schema.Nodes = append(schema.Nodes, Node{Name: nodeName})
				curNode = &schema.Nodes[len(schema.Nodes)-1]
				curEmit = nil
				curListen = nil
				state = sNodeBody
				continue
			}
			if indent == 1 && strings.HasSuffix(trimmed, ":") && !isListItem(trimmed) {
				nodeName := trimmed[:len(trimmed)-1]

				// Duplicate node detection
				if firstLine, exists := seenNodes[nodeName]; exists {
					return nil, parseError(manifestPath, lineno, "duplicate node name '%s' (first declared at line %d)", nodeName, firstLine)
				}
				seenNodes[nodeName] = lineno

				n := Node{Name: nodeName}
				schema.Nodes = append(schema.Nodes, n)
				curNode = &schema.Nodes[len(schema.Nodes)-1]
				curEmit = nil
				curListen = nil
				nodeShift = 0
				state = sNodeBody
				continue
			}

			eff := indent - nodeShift
			if eff == 2 && curNode != nil {
				switch trimmed {
				case "owns_files:":
					state = sNodeOwnFiles
					continue
				case "emits:":
					state = sNodeEmits
					continue
				case "listens:":
					state = sNodeListens
					continue
				}
			}

			if state == sNodeOwnFiles && eff == 3 && isListItem(trimmed) && curNode != nil {
				curNode.OwnsFiles = append(curNode.OwnsFiles, unquote(trimmed[2:]))
				continue
			}

			if state == sNodeEmits && eff == 3 {
				if v, ok := listKvValue(trimmed, "event"); ok && curNode != nil {
					curNode.Emits = append(curNode.Emits, Emit{Event: unquote(v)})
					curEmit = &curNode.Emits[len(curNode.Emits)-1]
					state = sNodeEmitEntry
					continue
				}
			}

			if (state == sNodeEmitEntry || state == sNodeEmitPayload) && eff == 4 {
				if trimmed == "payload:" {
					state = sNodeEmitPayload
					continue
				}
			}

			if state == sNodeEmitPayload && eff == 5 && curEmit != nil {
				if idx := strings.Index(trimmed, ":"); idx > 0 {
					curEmit.Fields = append(curEmit.Fields, PayloadField{
						Name:      strings.TrimSpace(trimmed[:idx]),
						FieldType: fieldTypeValue(trimmed[idx+1:]),
					})
					continue
				}
			}

			if state == sNodeListens && eff == 3 {
				if v, ok := listKvValue(trimmed, "state"); ok && curNode != nil {
					curNode.Listens = append(curNode.Listens, Listen{State: unquote(v)})
					curListen = &curNode.Listens[len(curNode.Listens)-1]
					state = sNodeListenEntry
					continue
				}
			}

			if (state == sNodeListenEntry || state == sNodeListenPayload) && eff == 4 {
				if trimmed == "payload:" {
					state = sNodeListenPayload
					continue
				}
			}

			if state == sNodeListenPayload && eff == 5 && curListen != nil {
				if idx := strings.Index(trimmed, ":"); idx > 0 {
					curListen.Fields = append(curListen.Fields, PayloadField{
						Name:      strings.TrimSpace(trimmed[:idx]),
						FieldType: fieldTypeValue(trimmed[idx+1:]),
					})
					continue
				}
			}

			// Transitions
			if eff == 3 && (state == sNodeEmitEntry || state == sNodeEmitPayload) {
				if v, ok := listKvValue(trimmed, "event"); ok && curNode != nil {
					curNode.Emits = append(curNode.Emits, Emit{Event: unquote(v)})
					curEmit = &curNode.Emits[len(curNode.Emits)-1]
					state = sNodeEmitEntry
					continue
				}
				if v, ok := listKvValue(trimmed, "state"); ok && curNode != nil {
					curNode.Listens = append(curNode.Listens, Listen{State: unquote(v)})
					curListen = &curNode.Listens[len(curNode.Listens)-1]
					state = sNodeListenEntry
					continue
				}
			}
			if eff == 3 && (state == sNodeListenEntry || state == sNodeListenPayload) {
				if v, ok := listKvValue(trimmed, "state"); ok && curNode != nil {
					curNode.Listens = append(curNode.Listens, Listen{State: unquote(v)})
					curListen = &curNode.Listens[len(curNode.Listens)-1]
					state = sNodeListenEntry
					continue
				}
			}

			if eff == 2 && curNode != nil && state >= sNodeOwnFiles && state <= sNodeListenPayload {
				state = sNodeBody
				curEmit = nil
				curListen = nil
				switch trimmed {
				case "owns_files:":
					state = sNodeOwnFiles
					continue
				case "emits:":
					state = sNodeEmits
					continue
				case "listens:":
					state = sNodeListens
					continue
				}
			}

			continue
		}

		// ===== ROUTES =====
		if state >= sRoutes && state <= sRouteStepBody {
			if indent == 1 && isListItem(trimmed) {
				afterDash := trimmed[2:]
				var val string
				var found bool
				if strings.HasPrefix(afterDash, "\"on\":") {
					val = strings.TrimSpace(afterDash[5:])
					found = true
				} else if strings.HasPrefix(afterDash, "on:") {
					val = strings.TrimSpace(afterDash[3:])
					found = true
				}
				if found {
					schema.Routes = append(schema.Routes, Route{OnEvent: unquote(val)})
					curRoute = &schema.Routes[len(schema.Routes)-1]
					curStep = nil
					state = sRouteBody
					continue
				}
			}

			if indent == 2 && curRoute != nil && (state == sRouteBody || state == sRouteSteps || state == sRouteStepBody) {
				if trimmed == "steps:" {
					state = sRouteSteps
					continue
				}
				if v, ok := kvValue(trimmed, "if"); ok {
					curRoute.IfCondition = unquote(v)
					state = sRouteBody
					continue
				}
				if v, ok := kvValue(trimmed, "cron"); ok {
					curRoute.Cron = unquote(v)
					state = sRouteBody
					continue
				}
				if v, ok := kvValue(trimmed, "parallel"); ok {
					parallel, perr := parseBoolFlag(manifestPath, lineno, "parallel", v)
					if perr != nil {
						return nil, perr
					}
					curRoute.Parallel = parallel
					state = sRouteBody
					continue
				}
				if v, ok := kvValue(trimmed, "emit"); ok {
					curRoute.EmitState = unquote(v)
					state = sRouteBody
					continue
				}
				if v, ok := kvValue(trimmed, "on_failure"); ok {
					curRoute.OnFailure = unquote(v)
					state = sRouteBody
					continue
				}
				if v, ok := kvValue(trimmed, "on_error"); ok {
					curRoute.OnFailure = unquote(v)
					state = sRouteBody
					continue
				}
			}

			if (state == sRouteSteps || state == sRouteStepBody) && indent == 3 && curRoute != nil {
				if v, ok := listKvValue(trimmed, "action"); ok {
					curRoute.Steps = append(curRoute.Steps, RouteStep{Action: unquote(v)})
					curStep = &curRoute.Steps[len(curRoute.Steps)-1]
					state = sRouteStepBody
					continue
				}
			}

			if state == sRouteStepBody && indent == 5 && isListItem(trimmed) && curStep != nil {
				// List item under `key:` — comma-join into a single Config
				// value so the engine sees one string: "slot_id,customer_email".
				if curStep.Config == nil {
					curStep.Config = make(map[string]string)
				}
				if prev := curStep.Config["key"]; prev != "" {
					curStep.Config["key"] = prev + "," + unquote(trimmed[2:])
				} else {
					curStep.Config["key"] = unquote(trimmed[2:])
				}
				continue
			}
			if state == sRouteStepBody && indent == 4 && curStep != nil {
				if v, ok := kvValue(trimmed, "if"); ok {
					curStep.IfCondition = unquote(v)
					continue
				}
				if v, ok := kvValue(trimmed, "max_attempts"); ok {
					if attempts, err := strconv.Atoi(unquote(v)); err == nil {
						curStep.MaxAttempts = attempts
					}
					continue
				}
				if v, ok := kvValue(trimmed, "backoff_ms"); ok {
					if backoff, err := strconv.Atoi(unquote(v)); err == nil {
						curStep.BackoffMs = backoff
					}
					continue
				}
				if v, ok := kvValue(trimmed, "table"); ok {
					curStep.Table = unquote(v)
					continue
				}
				if v, ok := kvValue(trimmed, "input"); ok {
					curStep.Input = unquote(v)
					continue
				}
				if v, ok := kvValue(trimmed, "url"); ok {
					curStep.URL = unquote(v)
					continue
				}
				if v, ok := kvValue(trimmed, "message"); ok {
					curStep.Message = unquote(v)
					continue
				}
				if v, ok := kvValue(trimmed, "where"); ok {
					curStep.Where = unquote(v)
					continue
				}
				if v, ok := kvValue(trimmed, "on_failure"); ok {
					curStep.OnFailure = unquote(v)
					continue
				}
				if v, ok := kvValue(trimmed, "compensate"); ok {
					curStep.Compensate = unquote(v)
					continue
				}
				if v, ok := kvValue(trimmed, "on_error"); ok {
					curStep.OnFailure = unquote(v)
					continue
				}
				if v, ok := kvValue(trimmed, "key"); ok {
					// Composite keys: `key:` with an empty value opens a list of
					// column names at indent 5 (list items captured below).
					// A bare string stays scalar for backward compatibility.
					if strings.TrimSpace(v) != "" {
						if curStep.Config == nil {
							curStep.Config = make(map[string]string)
						}
						curStep.Config["key"] = unquote(v)
					}
					continue
				}
				// Capture unknown key:value pairs into Config map for custom actions
				if idx := strings.Index(trimmed, ":"); idx > 0 {
					key := strings.TrimSpace(trimmed[:idx])
					val := strings.TrimSpace(trimmed[idx+1:])
					if curStep.Config == nil {
						curStep.Config = make(map[string]string)
					}
					curStep.Config[key] = unquote(val)
					continue
				}
			}

			continue
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading manifest: %w", err)
	}

	// NOTE: semantic validation (validateSchema) intentionally runs in
	// ParseManifest AFTER includes are merged, so routes can reference events
	// declared in included files. A per-frame validation here would reject
	// that natural include pattern with a misleading "possible typo?" error.

	// Process includes with circular detection
	newStack := append(includeStack, manifestPath)
	baseDir := filepath.Dir(manifestPath)
	for _, incRel := range schema.Includes {
		incPath := filepath.Join(baseDir, incRel)
		absIncPath, err := filepath.Abs(incPath)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve include path '%s': %w", incPath, err)
		}

		subSchema, err := parseManifestWithStack(absIncPath, newStack)
		if err != nil {
			return nil, fmt.Errorf("failed to process included manifest '%s': %w", incRel, err)
		}

		// An included file's own spine_version is dropped by the merge, so
		// range-check it here (the top-level version is validated post-merge).
		if subSchema.SpineVersion < 1 || subSchema.SpineVersion > MaxSupportedSpineVersion {
			return nil, fmt.Errorf("included manifest '%s' declares unsupported 'spine_version: %d' — this engine supports manifest schema versions 1 to %d", incRel, subSchema.SpineVersion, MaxSupportedSpineVersion)
		}

		// Deduplicate tables from includes
		for _, t := range subSchema.DbTables {
			if !seenTables[t] {
				schema.DbTables = append(schema.DbTables, t)
				seenTables[t] = true
			}
		}

		// Node names must be unique across the whole manifest graph: a
		// duplicate silently last-wins in the registry today.
		for _, n := range subSchema.Nodes {
			if _, dup := seenNodes[n.Name]; dup {
				return nil, fmt.Errorf("included manifest '%s' declares node '%s' which is already defined in this manifest graph — node names must be unique", incRel, n.Name)
			}
			seenNodes[n.Name] = 0
		}

		schema.Nodes = append(schema.Nodes, subSchema.Nodes...)
		schema.Routes = append(schema.Routes, subSchema.Routes...)
	}

	return schema, nil
}

// ValidateSchema performs post-parse semantic validation on the parsed schema.
func ValidateSchema(file string, schema *SpineSchema) error {
	// Missing spine_version
	if schema.SpineVersion == 0 {
		return parseError(file, 0, "missing required 'spine_version' declaration")
	}

	// Unsupported manifest format: refuse versions newer than this engine
	// speaks (or negative garbage) — a manifest written for a future runtime
	// must fail loudly at startup, not misbehave silently.
	if schema.SpineVersion < 1 || schema.SpineVersion > MaxSupportedSpineVersion {
		return parseError(file, 0, "unsupported 'spine_version: %d' — this engine supports manifest schema versions 1 to %d", schema.SpineVersion, MaxSupportedSpineVersion)
	}

	// Capability tiers: gated actions demand the manifest declare at least
	// the version that introduced them.
	for _, route := range schema.Routes {
		for j, step := range route.Steps {
			if minV, gated := actionMinVersion[step.Action]; gated && schema.SpineVersion < minV {
				return parseError(file, 0,
					"route '%s', step %d: action '%s' requires 'spine_version: %d' (manifest declares %d) — raise spine_version to unlock it",
					route.OnEvent, j+1, step.Action, minV, schema.SpineVersion)
			}
		}
	}

	// Build set of known emitted events from nodes
	knownEvents := make(map[string]bool)
	for _, node := range schema.Nodes {
		for _, e := range node.Emits {
			knownEvents[e.Event] = true
		}
	}

	// Duplicate event declarations must agree on their payload shape. The
	// registry (last declaration wins) and the TS codegen (most fields win)
	// would otherwise diverge: generated types could require fields the
	// runtime never validates, or vice versa. Identical re-declarations
	// (same fields, same types — e.g. two nodes emitting the same event) are
	// fine; conflicting shapes are a manifest bug and fail loudly.
	type eventShape map[string]string // field -> type
	seenShapes := make(map[string]eventShape)
	for _, node := range schema.Nodes {
		for _, e := range node.Emits {
			shape := make(eventShape, len(e.Fields))
			for _, f := range e.Fields {
				shape[f.Name] = strings.ToLower(f.FieldType)
			}
			if prev, ok := seenShapes[e.Event]; ok {
				// An empty declaration (no fields) is a no-op, not a conflict.
				if len(prev) > 0 && len(shape) > 0 && !equalEventShape(prev, shape) {
					return parseError(file, 0,
						"event '%s' is declared with conflicting payload shapes by multiple nodes (%v vs %v) — duplicate event declarations must match",
						e.Event, prev, shape)
				}
			} else {
				seenShapes[e.Event] = shape
			}
		}
	}

	for i, route := range schema.Routes {
		// Empty action in step
		for j, step := range route.Steps {
			if step.Action == "" {
				return parseError(file, 0, "route %d, step %d has an empty 'action' field", i+1, j+1)
			}
		}

		// Route with no steps
		if len(route.Steps) == 0 {
			return parseError(file, 0, "route on '%s' has no steps defined", route.OnEvent)
		}

		// Route referencing an event not declared by any node (warning-grade: only if nodes exist)
		if len(schema.Nodes) > 0 && !knownEvents[route.OnEvent] {
			// Check if the route.OnEvent matches an emitted state or failure state (chained events are valid)
			isChainedState := false
			for _, r := range schema.Routes {
				if r.EmitState == route.OnEvent || r.OnFailure == route.OnEvent {
					isChainedState = true
					break
				}
				for _, step := range r.Steps {
					if step.OnFailure == route.OnEvent {
						isChainedState = true
						break
					}
				}
				if isChainedState {
					break
				}
			}
			if !isChainedState {
				var declaredList []string
				for ev := range knownEvents {
					declaredList = append(declaredList, ev)
				}
				suggestion := suggestSimilarKey(route.OnEvent, declaredList)
				if suggestion != "" {
					return parseError(file, 0, "route references event '%s' which is not declared in any node's emits. Did you mean '%s'?", route.OnEvent, suggestion)
				}
				return parseError(file, 0, "route references event '%s' which is not declared in any node's emits (possible typo?)", route.OnEvent)
			}
		}
	}

	return nil
}

// equalEventShape reports whether two event payload shapes (field → type)
// describe the same contract.
func equalEventShape(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func suggestSimilarKey(input string, validKeys []string) string {
	bestMatch := ""
	minDist := 3

	for _, k := range validKeys {
		dist := levDistance(strings.ToLower(input), strings.ToLower(k))
		if dist < minDist {
			minDist = dist
			bestMatch = k
		}
	}
	return bestMatch
}

func levDistance(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	v0 := make([]int, lb+1)
	v1 := make([]int, lb+1)
	for i := 0; i <= lb; i++ {
		v0[i] = i
	}
	for i := 0; i < la; i++ {
		v1[0] = i + 1
		for j := 0; j < lb; j++ {
			cost := 0
			if a[i] != b[j] {
				cost = 1
			}
			v1[j+1] = min(v0[j+1]+1, min(v1[j]+1, v0[j]+cost))
		}
		for j := 0; j <= lb; j++ {
			v0[j] = v1[j]
		}
	}
	return v0[lb]
}
