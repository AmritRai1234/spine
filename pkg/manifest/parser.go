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
// with an empty include stack for circular include detection.
func ParseManifest(manifestPath string) (*SpineSchema, error) {
	absPath, err := filepath.Abs(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve path '%s': %w", manifestPath, err)
	}
	return parseManifestWithStack(absPath, nil)
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

	// Duplicate tracking
	seenNodes := make(map[string]int)  // node name → first line number
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
						curAccess.Key = keyVal
						state = sAccessEntry
						continue
					}
					if v, ok := kvValue(trimmed, "tenant"); ok {
						curAccess.Tenant = unquote(v)
						state = sAccessEntry
						continue
					}
					if v, ok := kvValue(trimmed, "read_only"); ok {
						curAccess.ReadOnly = (v == "true")
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
				state = sNodeBody
				continue
			}

			if indent == 2 && curNode != nil {
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

			if state == sNodeOwnFiles && indent == 3 && isListItem(trimmed) && curNode != nil {
				curNode.OwnsFiles = append(curNode.OwnsFiles, unquote(trimmed[2:]))
				continue
			}

			if state == sNodeEmits && indent == 3 {
				if v, ok := listKvValue(trimmed, "event"); ok && curNode != nil {
					curNode.Emits = append(curNode.Emits, Emit{Event: unquote(v)})
					curEmit = &curNode.Emits[len(curNode.Emits)-1]
					state = sNodeEmitEntry
					continue
				}
			}

			if (state == sNodeEmitEntry || state == sNodeEmitPayload) && indent == 4 {
				if trimmed == "payload:" {
					state = sNodeEmitPayload
					continue
				}
			}

			if state == sNodeEmitPayload && indent == 5 && curEmit != nil {
				if idx := strings.Index(trimmed, ":"); idx > 0 {
					curEmit.Fields = append(curEmit.Fields, PayloadField{
						Name:      strings.TrimSpace(trimmed[:idx]),
						FieldType: unquote(trimmed[idx+1:]),
					})
					continue
				}
			}

			if state == sNodeListens && indent == 3 {
				if v, ok := listKvValue(trimmed, "state"); ok && curNode != nil {
					curNode.Listens = append(curNode.Listens, Listen{State: unquote(v)})
					curListen = &curNode.Listens[len(curNode.Listens)-1]
					state = sNodeListenEntry
					continue
				}
			}

			if (state == sNodeListenEntry || state == sNodeListenPayload) && indent == 4 {
				if trimmed == "payload:" {
					state = sNodeListenPayload
					continue
				}
			}

			if state == sNodeListenPayload && indent == 5 && curListen != nil {
				if idx := strings.Index(trimmed, ":"); idx > 0 {
					curListen.Fields = append(curListen.Fields, PayloadField{
						Name:      strings.TrimSpace(trimmed[:idx]),
						FieldType: unquote(trimmed[idx+1:]),
					})
					continue
				}
			}

			// Transitions
			if indent == 3 && (state == sNodeEmitEntry || state == sNodeEmitPayload) {
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
			if indent == 3 && (state == sNodeListenEntry || state == sNodeListenPayload) {
				if v, ok := listKvValue(trimmed, "state"); ok && curNode != nil {
					curNode.Listens = append(curNode.Listens, Listen{State: unquote(v)})
					curListen = &curNode.Listens[len(curNode.Listens)-1]
					state = sNodeListenEntry
					continue
				}
			}

			if indent == 2 && curNode != nil && state >= sNodeOwnFiles && state <= sNodeListenPayload {
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
					curRoute.Parallel = unquote(v) == "true"
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

	// Post-parse semantic validation
	if err := validateSchema(manifestPath, schema); err != nil {
		return nil, err
	}

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

		// Deduplicate tables from includes
		for _, t := range subSchema.DbTables {
			if !seenTables[t] {
				schema.DbTables = append(schema.DbTables, t)
				seenTables[t] = true
			}
		}
		schema.Nodes = append(schema.Nodes, subSchema.Nodes...)
		schema.Routes = append(schema.Routes, subSchema.Routes...)
	}

	return schema, nil
}

// validateSchema performs post-parse semantic validation on the parsed schema.
func validateSchema(file string, schema *SpineSchema) error {
	// Missing spine_version
	if schema.SpineVersion == 0 {
		return parseError(file, 0, "missing required 'spine_version' declaration")
	}

	// Build set of known emitted events from nodes
	knownEvents := make(map[string]bool)
	for _, node := range schema.Nodes {
		for _, e := range node.Emits {
			knownEvents[e.Event] = true
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
