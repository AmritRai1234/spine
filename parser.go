package spine

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

func getIndent(line string) int {
	count := 0
	for _, c := range line {
		if c == ' ' {
			count++
		} else {
			break
		}
	}
	return count / 2
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
func ParseManifest(manifestPath string) (*SpineSchema, error) {
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
		}

		// ===== INCLUDES =====
		if state == sIncludes {
			if indent >= 1 && isListItem(trimmed) {
				schema.Includes = append(schema.Includes, unquote(trimmed[2:]))
				continue
			}
		}

		// ===== DATABASE =====
		if state == sDatabase && indent == 1 && trimmed == "tables:" {
			state = sDbTables
			continue
		}
		if state == sDbTables {
			if indent == 2 && isListItem(trimmed) {
				schema.DbTables = append(schema.DbTables, unquote(trimmed[2:]))
				continue
			}
			if indent <= 1 {
				state = sTop
			}
		}

		// ===== NODES =====
		if state >= sNodes && state <= sNodeListenPayload {
			if indent == 1 && strings.HasSuffix(trimmed, ":") && !isListItem(trimmed) {
				n := Node{Name: trimmed[:len(trimmed)-1]}
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
			}

			continue
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading manifest: %w", err)
	}

	baseDir := filepath.Dir(manifestPath)
	for _, incRel := range schema.Includes {
		incPath := filepath.Join(baseDir, incRel)
		subSchema, err := ParseManifest(incPath)
		if err != nil {
			return nil, fmt.Errorf("failed to process included manifest '%s': %w", incPath, err)
		}
		schema.DbTables = append(schema.DbTables, subSchema.DbTables...)
		schema.Nodes = append(schema.Nodes, subSchema.Nodes...)
		schema.Routes = append(schema.Routes, subSchema.Routes...)
	}

	return schema, nil
}
