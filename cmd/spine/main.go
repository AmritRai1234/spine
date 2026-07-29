package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/pkg/manifest"
)

const version = "2.2.0"

func usage() {
	fmt.Fprintf(os.Stderr, `Spine — Declarative Event-Driven Backend Engine (v%s)

Usage:
  spine <command> [options]

Commands:
  serve   Start the Spine HTTP/WS server from a .spine manifest
  emit    Emit an event to a running Spine server
  parse   Validate and inspect a .spine manifest file
  version Print the current version

Run 'spine <command> --help' for command-specific usage.
`, version)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		cmdServe(os.Args[2:])
	case "emit":
		cmdEmit(os.Args[2:])
	case "parse":
		cmdParse(os.Args[2:])
	case "version":
		fmt.Printf("spine v%s (go runtime)\n", version)
	case "--help", "-h", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

// ─── serve ───────────────────────────────────────────────────────────────────

func cmdServe(args []string) {
	var (
		manifestPath string
		dbPath       string
		port         string
		apiKey       string
		rateLimit    float64
	)

	// Defaults
	dbPath = "spine.db"
	port = "8080"

	// Parse flags manually for clean CLI UX
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			fmt.Fprintf(os.Stderr, `Usage: spine serve <manifest.spine> [options]

Options:
  --port <port>       HTTP server port (default: 8080)
  --db <path>         Database path (default: spine.db)
  --api-key <key>     Require API key for protected endpoints
  --rate-limit <rps>  Enable rate limiting at N requests/sec
  -h, --help          Show this help

Environment:
  SPINE_API_KEY       API key (overrides --api-key)
  SPINE_PORT          Server port (overrides --port)
  SPINE_DB            Database path (overrides --db)

Examples:
  spine serve app.spine
  spine serve app.spine --port 3000 --api-key secret123
  spine serve app.spine --db turso://mydb.turso.io --rate-limit 1000
`)
			return
		case "--port":
			i++
			if i < len(args) {
				port = args[i]
			}
		case "--db":
			i++
			if i < len(args) {
				dbPath = args[i]
			}
		case "--api-key":
			i++
			if i < len(args) {
				apiKey = args[i]
			}
		case "--rate-limit":
			i++
			if i < len(args) {
				fmt.Sscanf(args[i], "%f", &rateLimit)
			}
		default:
			if !strings.HasPrefix(args[i], "-") && manifestPath == "" {
				manifestPath = args[i]
			} else {
				fmt.Fprintf(os.Stderr, "Unknown option: %s\n", args[i])
				os.Exit(1)
			}
		}
	}

	// Environment variable overrides
	if v := os.Getenv("SPINE_PORT"); v != "" {
		port = v
	}
	if v := os.Getenv("SPINE_DB"); v != "" {
		dbPath = v
	}
	if v := os.Getenv("SPINE_API_KEY"); v != "" {
		apiKey = v
	}

	if manifestPath == "" {
		fmt.Fprintln(os.Stderr, "Error: manifest file path required")
		fmt.Fprintln(os.Stderr, "Usage: spine serve <manifest.spine>")
		os.Exit(1)
	}

	// Parse manifest
	fmt.Printf("⚡ Spine v%s\n", version)
	fmt.Printf("   manifest: %s\n", manifestPath)
	fmt.Printf("   database: %s\n", dbPath)
	fmt.Printf("   port:     %s\n", port)

	eng, err := spine.NewFromFile(manifestPath, dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ Failed to initialize: %v\n", err)
		os.Exit(1)
	}
	defer eng.Close()

	schema := eng.Bus.GetRegistry().GetSchema()
	fmt.Printf("   nodes:    %d\n", len(schema.Nodes))
	fmt.Printf("   routes:   %d\n", len(schema.Routes))
	fmt.Printf("   tables:   %d\n", len(schema.DbTables))

	if apiKey != "" {
		eng.SetAPIKey(apiKey)
		fmt.Println("   auth:     API key enabled")
	}
	if rateLimit > 0 {
		eng.SetRateLimit(rateLimit, rateLimit*2)
		fmt.Printf("   rate:     %.0f req/s\n", rateLimit)
	}

	addr := ":" + port
	fmt.Printf("\n✓ Listening on http://0.0.0.0%s\n", addr)
	fmt.Println("  Press Ctrl+C to stop")

	if err := eng.ListenAndServe(addr); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "✗ Server error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Shutdown complete")
}

// ─── emit ────────────────────────────────────────────────────────────────────

func cmdEmit(args []string) {
	var (
		serverURL string
		event     string
		payload   string
		apiKey    string
	)

	serverURL = "http://localhost:8080"

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			fmt.Fprintf(os.Stderr, `Usage: spine emit <event_name> [options]

Options:
  --payload <json>    JSON payload string (default: {})
  --server <url>      Spine server URL (default: http://localhost:8080)
  --api-key <key>     API key for authenticated servers
  -h, --help          Show this help

Examples:
  spine emit USER_LOGIN --payload '{"email":"test@dev.com"}'
  spine emit CREATE_ORDER --server http://prod:8080 --api-key secret
`)
			return
		case "--payload":
			i++
			if i < len(args) {
				payload = args[i]
			}
		case "--server":
			i++
			if i < len(args) {
				serverURL = args[i]
			}
		case "--api-key":
			i++
			if i < len(args) {
				apiKey = args[i]
			}
		default:
			if !strings.HasPrefix(args[i], "-") && event == "" {
				event = args[i]
			}
		}
	}

	if event == "" {
		fmt.Fprintln(os.Stderr, "Error: event name required")
		fmt.Fprintln(os.Stderr, "Usage: spine emit <event_name> [--payload '{...}']")
		os.Exit(1)
	}

	if payload == "" {
		payload = "{}"
	}

	// Build request body
	body := fmt.Sprintf(`{"event":"%s","payload":%s}`, event, payload)
	req, err := http.NewRequest("POST", serverURL+"/emit", strings.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ Request error: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ Connection error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	out, _ := json.MarshalIndent(result, "", "  ")
	if resp.StatusCode >= 400 {
		fmt.Fprintf(os.Stderr, "✗ [%d] %s\n", resp.StatusCode, string(out))
		os.Exit(1)
	}
	fmt.Printf("✓ [%d] %s\n", resp.StatusCode, string(out))
}

// ─── parse ───────────────────────────────────────────────────────────────────

func cmdParse(args []string) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintf(os.Stderr, `Usage: spine parse <manifest.spine> [--json]

Options:
  --json    Output parsed schema as JSON
  -h        Show this help

Examples:
  spine parse app.spine
  spine parse app.spine --json
`)
		return
	}

	manifestPath := args[0]
	outputJSON := false
	for _, a := range args[1:] {
		if a == "--json" {
			outputJSON = true
		}
	}

	schema, err := manifest.ParseManifest(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ Parse error: %v\n", err)
		os.Exit(1)
	}

	if outputJSON {
		out, _ := json.MarshalIndent(schema, "", "  ")
		fmt.Println(string(out))
		return
	}

	fmt.Printf("✓ Manifest parsed successfully: %s\n\n", manifestPath)
	fmt.Printf("  spine_version: %d\n", schema.SpineVersion)
	fmt.Printf("  tables:        %d\n", len(schema.DbTables))
	for _, t := range schema.DbTables {
		fmt.Printf("    - %s\n", t)
	}
	fmt.Printf("  nodes:         %d\n", len(schema.Nodes))
	for _, n := range schema.Nodes {
		fmt.Printf("    - %s (emits: %d, listens: %d)\n", n.Name, len(n.Emits), len(n.Listens))
	}
	fmt.Printf("  routes:        %d\n", len(schema.Routes))
	for _, r := range schema.Routes {
		guard := ""
		if r.IfCondition != "" {
			guard = fmt.Sprintf(" [if: %s]", r.IfCondition)
		}
		parallel := ""
		if r.Parallel {
			parallel = " ⚡parallel"
		}
		fmt.Printf("    - on: %s → %d steps%s%s", r.OnEvent, len(r.Steps), guard, parallel)
		if r.EmitState != "" {
			fmt.Printf(" → emit: %s", r.EmitState)
		}
		fmt.Println()
	}
	if len(schema.Includes) > 0 {
		fmt.Printf("  includes:      %d\n", len(schema.Includes))
		for _, inc := range schema.Includes {
			fmt.Printf("    - %s\n", inc)
		}
	}
}
