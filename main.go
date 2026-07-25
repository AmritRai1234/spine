// spine-go/main.go — Spine v1 Runtime (Go)
//
// Same architecture as C/Rust:
//   Parse → Registry → Bus (emit/dispatch) → SQLite → HTTP server

package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// =====================================================================
//  Schema structs
// =====================================================================

type PayloadField struct {
	Name      string
	FieldType string
}

type Emit struct {
	Event  string
	Fields []PayloadField
}

type Listen struct {
	State  string
	Fields []PayloadField
}

type Node struct {
	Name      string
	OwnsFiles []string
	Emits     []Emit
	Listens   []Listen
}

type RouteStep struct {
	Action string
	Table  string
	Input  string
}

type Route struct {
	OnEvent   string
	Steps     []RouteStep
	EmitState string
}

type SpineSchema struct {
	SpineVersion int
	DbTables     []string
	Nodes        []Node
	Routes       []Route
}

// =====================================================================
//  Parser — line-by-line state machine
// =====================================================================

type parseState int

const (
	sTop parseState = iota
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

func parseSpine(filepath string) *SpineSchema {
	f, err := os.Open(filepath)
	if err != nil {
		log.Fatalf("spine: cannot open '%s': %v", filepath, err)
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
			// New node
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
						Name:      trimmed[:idx],
						FieldType: strings.TrimSpace(trimmed[idx+1:]),
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
						Name:      trimmed[:idx],
						FieldType: strings.TrimSpace(trimmed[idx+1:]),
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
				if v, ok := kvValue(trimmed, "table"); ok {
					curStep.Table = unquote(v)
					continue
				}
				if v, ok := kvValue(trimmed, "input"); ok {
					curStep.Input = unquote(v)
					continue
				}
			}

			continue
		}
	}

	return schema
}

// =====================================================================
//  Registry
// =====================================================================

type Registry struct {
	schema *SpineSchema
	nodes  map[string]*Node
	routes map[string][]*Route
}

func buildRegistry(schema *SpineSchema) *Registry {
	reg := &Registry{
		schema: schema,
		nodes:  make(map[string]*Node),
		routes: make(map[string][]*Route),
	}
	for i := range schema.Nodes {
		reg.nodes[schema.Nodes[i].Name] = &schema.Nodes[i]
	}
	for i := range schema.Routes {
		r := &schema.Routes[i]
		reg.routes[r.OnEvent] = append(reg.routes[r.OnEvent], r)
	}
	return reg
}

// =====================================================================
//  Bus
// =====================================================================

type Bus struct {
	registry *Registry
	db       *sql.DB
}

func newBus(reg *Registry, dbPath string) *Bus {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		log.Fatalf("spine-go: cannot open sqlite: %v", err)
	}
	return &Bus{registry: reg, db: db}
}

func (b *Bus) emit(event string, payload map[string]interface{}) map[string]interface{} {
	routes, ok := b.registry.routes[event]
	if !ok || len(routes) == 0 {
		return map[string]interface{}{
			"status":         "no_route",
			"event":          event,
			"routes_matched": 0,
		}
	}

	for _, route := range routes {
		for _, step := range route.Steps {
			b.execStep(&step, payload)
		}
	}

	return map[string]interface{}{
		"status":         "ok",
		"event":          event,
		"routes_matched": len(routes),
	}
}

func (b *Bus) execStep(step *RouteStep, payload map[string]interface{}) {
	if step.Action == "db.insert" && step.Table != "" {
		b.dbInsert(step.Table, payload)
	} else if step.Action == "db.update" && step.Table != "" {
		b.dbUpdate(step.Table, payload)
	}
}

func (b *Bus) dbInsert(table string, payload map[string]interface{}) {
	if len(payload) == 0 {
		return
	}

	// Ensure table
	var colDefs []string
	var colNames []string
	var placeholders []string
	var values []interface{}

	for k, v := range payload {
		colDefs = append(colDefs, fmt.Sprintf(`"%s" TEXT`, k))
		colNames = append(colNames, fmt.Sprintf(`"%s"`, k))
		placeholders = append(placeholders, "?")
		values = append(values, fmt.Sprintf("%v", v))
	}

	createSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "%s" (id INTEGER PRIMARY KEY AUTOINCREMENT, %s)`,
		table, strings.Join(colDefs, ", "))
	b.db.Exec(createSQL)

	insertSQL := fmt.Sprintf(`INSERT INTO "%s" (%s) VALUES (%s)`,
		table, strings.Join(colNames, ", "), strings.Join(placeholders, ", "))
	b.db.Exec(insertSQL, values...)
}

func (b *Bus) dbUpdate(table string, payload map[string]interface{}) {
	if len(payload) < 2 {
		return
	}

	var colDefs []string
	var keys []string
	var vals []interface{}

	for k, v := range payload {
		colDefs = append(colDefs, fmt.Sprintf(`"%s" TEXT`, k))
		keys = append(keys, k)
		vals = append(vals, fmt.Sprintf("%v", v))
	}

	createSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "%s" (id INTEGER PRIMARY KEY AUTOINCREMENT, %s)`,
		table, strings.Join(colDefs, ", "))
	b.db.Exec(createSQL)

	// First key = WHERE, rest = SET
	var setClauses []string
	var params []interface{}
	for i := 1; i < len(keys); i++ {
		setClauses = append(setClauses, fmt.Sprintf(`"%s" = ?`, keys[i]))
		params = append(params, vals[i])
	}
	params = append(params, vals[0])

	updateSQL := fmt.Sprintf(`UPDATE "%s" SET %s WHERE "%s" = ?`,
		table, strings.Join(setClauses, ", "), keys[0])
	b.db.Exec(updateSQL, params...)
}

// =====================================================================
//  HTTP Server
// =====================================================================

func startServer(bus *Bus, port string) {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","engine":"spine-go","version":1}`))
	})

	mux.HandleFunc("/emit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(405)
			w.Write([]byte(`{"error":"method not allowed"}`))
			return
		}

		var body struct {
			Event   string                 `json:"event"`
			Payload map[string]interface{} `json:"payload"`
		}

		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"invalid JSON"}`))
			return
		}

		if body.Payload == nil {
			body.Payload = make(map[string]interface{})
		}

		result := bus.emit(body.Event, body.Payload)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})

	fmt.Println()
	fmt.Println("┌──────────────────────────────────────────┐")
	fmt.Println("│  SPINE v1 Runtime Server (Go)            │")
	fmt.Printf("│  Listening: http://0.0.0.0:%-14s │\n", port)
	fmt.Println("└──────────────────────────────────────────┘")
	fmt.Println()

	log.Fatal(http.ListenAndServe("0.0.0.0:"+port, mux))
}

// =====================================================================
//  Main
// =====================================================================

func main() {
	spineFile := "examples/app.spine"
	port := "8080"
	dbPath := "spine.db"

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port":
			if i+1 < len(args) {
				port = args[i+1]
				i++
			}
		case "--db":
			if i+1 < len(args) {
				dbPath = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "-") {
				spineFile = args[i]
			}
		}
	}

	fmt.Printf("[spine-go] parsing '%s'...\n", spineFile)
	schema := parseSpine(spineFile)
	fmt.Printf("[spine-go] loaded: %d nodes, %d routes, %d tables\n",
		len(schema.Nodes), len(schema.Routes), len(schema.DbTables))

	reg := buildRegistry(schema)
	bus := newBus(reg, dbPath)
	defer bus.db.Close()

	startServer(bus, port)
}
