package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/pkg/manifest"
)

// ============================================================
// Parser Benchmarks
// ============================================================

// BenchmarkParseSmallManifest benchmarks parsing a small manifest (3 nodes, 4 routes).
func BenchmarkParseSmallManifest(b *testing.B) {
	dir := b.TempDir()
	content := `spine_version: 1

database:
  tables:
    - users
    - orders
    - logs

nodes:
  LoginPage:
    emits:
      - event: USER_LOGIN
        payload:
          email: string
          password: string
    listens:
      - state: AUTH_STATUS
        payload:
          status: string

  Dashboard:
    emits:
      - event: CREATE_ORDER
        payload:
          item: string
          quantity: number

  AdminPanel:
    emits:
      - event: ADMIN_ACTION
        payload:
          action: string
          target: string

routes:
  - on: USER_LOGIN
    steps:
      - action: db.insert
        table: users
    emit: AUTH_STATUS

  - on: CREATE_ORDER
    parallel: true
    steps:
      - action: db.insert
        table: orders
      - action: log.write
        message: "Order created"
    emit: ORDER_CREATED

  - on: AUTH_STATUS
    steps:
      - action: log.write
        message: "Auth processed"

  - on: ADMIN_ACTION
    if: "$event.payload.action == 'delete'"
    steps:
      - action: db.delete
        table: logs
        where: "id = '$event.payload.target'"
`
	path := filepath.Join(dir, "small.spine")
	os.WriteFile(path, []byte(content), 0644)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := manifest.ParseManifest(path)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseLargeManifest benchmarks parsing a larger manifest (~20 nodes, ~20 routes).
func BenchmarkParseLargeManifest(b *testing.B) {
	dir := b.TempDir()

	var sb strings.Builder
	sb.WriteString("spine_version: 1\n\ndatabase:\n  tables:\n")
	for i := 0; i < 20; i++ {
		sb.WriteString(fmt.Sprintf("    - table_%d\n", i))
	}
	sb.WriteString("\nnodes:\n")
	for i := 0; i < 20; i++ {
		sb.WriteString(fmt.Sprintf(`  Node%d:
    emits:
      - event: EVENT_%d
        payload:
          id: string
          name: string
          value: number
    listens:
      - state: STATE_%d
        payload:
          result: string

`, i, i, i))
	}
	sb.WriteString("routes:\n")
	for i := 0; i < 20; i++ {
		sb.WriteString(fmt.Sprintf(`  - on: EVENT_%d
    steps:
      - action: db.insert
        table: table_%d
      - action: log.write
        message: "Processed event %d"
    emit: STATE_%d

`, i, i, i, i))
	}

	path := filepath.Join(dir, "large.spine")
	os.WriteFile(path, []byte(sb.String()), 0644)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := manifest.ParseManifest(path)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseWithIncludes benchmarks parsing a manifest that includes 2 sub-manifests.
func BenchmarkParseWithIncludes(b *testing.B) {
	dir := b.TempDir()

	for _, name := range []string{"auth", "billing"} {
		sub := fmt.Sprintf(`spine_version: 1

database:
  tables:
    - %s_records

nodes:
  %sNode:
    emits:
      - event: %s_EVENT
        payload:
          id: string
          data: string

routes:
  - on: %s_EVENT
    steps:
      - action: db.insert
        table: %s_records
`, name, strings.Title(name), strings.ToUpper(name), strings.ToUpper(name), name)
		os.WriteFile(filepath.Join(dir, name+".spine"), []byte(sub), 0644)
	}

	main := `spine_version: 1

includes:
  - auth.spine
  - billing.spine

database:
  tables:
    - main_data

nodes:
  MainNode:
    emits:
      - event: MAIN_EVENT
        payload:
          key: string

routes:
  - on: MAIN_EVENT
    steps:
      - action: db.insert
        table: main_data
`
	mainPath := filepath.Join(dir, "main.spine")
	os.WriteFile(mainPath, []byte(main), 0644)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := manifest.ParseManifest(mainPath)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseTabIndentation benchmarks parsing with tab indentation.
func BenchmarkParseTabIndentation(b *testing.B) {
	dir := b.TempDir()
	content := "spine_version: 1\n\ndatabase:\n\ttables:\n\t\t- tab_users\n\nnodes:\n\tTabNode:\n\t\temits:\n\t\t\t- event: TAB_EVENT\n\t\t\t\tpayload:\n\t\t\t\t\tname: string\n\nroutes:\n\t- on: TAB_EVENT\n\t\tsteps:\n\t\t\t- action: db.insert\n\t\t\t\ttable: tab_users\n"
	path := filepath.Join(dir, "tabs.spine")
	os.WriteFile(path, []byte(content), 0644)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := manifest.ParseManifest(path)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// ============================================================
// Engine Emit Pipeline Benchmarks
// ============================================================

// BenchmarkEmitSingle benchmarks a single event emission through the full pipeline.
func BenchmarkEmitSingle(b *testing.B) {
	dir := b.TempDir()
	content := `spine_version: 1

database:
  tables:
    - bench_table

nodes:
  BenchNode:
    emits:
      - event: BENCH_EVENT
        payload:
          name: string

routes:
  - on: BENCH_EVENT
    steps:
      - action: db.insert
        table: bench_table
`
	mPath := filepath.Join(dir, "bench.spine")
	os.WriteFile(mPath, []byte(content), 0644)
	dbPath := filepath.Join(dir, "bench.db")

	eng, err := spine.NewFromFile(mPath, dbPath)
	if err != nil {
		b.Fatal(err)
	}
	defer eng.Close()

	payload := map[string]interface{}{"name": "benchuser"}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := eng.Bus.Emit("BENCH_EVENT", payload)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEmitParallel benchmarks concurrent event emissions from multiple goroutines.
func BenchmarkEmitParallel(b *testing.B) {
	dir := b.TempDir()
	content := `spine_version: 1

database:
  tables:
    - parallel_table

nodes:
  ParNode:
    emits:
      - event: PAR_EVENT
        payload:
          id: string

routes:
  - on: PAR_EVENT
    steps:
      - action: db.insert
        table: parallel_table
`
	mPath := filepath.Join(dir, "par.spine")
	os.WriteFile(mPath, []byte(content), 0644)
	dbPath := filepath.Join(dir, "par.db")

	eng, err := spine.NewFromFile(mPath, dbPath)
	if err != nil {
		b.Fatal(err)
	}
	defer eng.Close()

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		payload := map[string]interface{}{"id": "goroutine-bench"}
		for pb.Next() {
			eng.Bus.Emit("PAR_EVENT", payload)
		}
	})
}

// BenchmarkEmitWithValidation benchmarks emit with typed payload contract validation.
func BenchmarkEmitWithValidation(b *testing.B) {
	dir := b.TempDir()
	content := `spine_version: 1

database:
  tables:
    - validated

nodes:
  ValNode:
    emits:
      - event: VAL_EVENT
        payload:
          email: string
          count: number
          active: boolean

routes:
  - on: VAL_EVENT
    steps:
      - action: db.insert
        table: validated
`
	mPath := filepath.Join(dir, "val.spine")
	os.WriteFile(mPath, []byte(content), 0644)
	dbPath := filepath.Join(dir, "val.db")

	eng, err := spine.NewFromFile(mPath, dbPath)
	if err != nil {
		b.Fatal(err)
	}
	defer eng.Close()

	payload := map[string]interface{}{
		"email":  "bench@test.dev",
		"count":  float64(42),
		"active": true,
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := eng.Bus.Emit("VAL_EVENT", payload)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRegistryLookup benchmarks the lock-free route registry lookup.
func BenchmarkRegistryLookup(b *testing.B) {
	dir := b.TempDir()

	var sb strings.Builder
	sb.WriteString("spine_version: 1\n\nnodes:\n")
	for i := 0; i < 50; i++ {
		sb.WriteString(fmt.Sprintf("  Node%d:\n    emits:\n      - event: EVT_%d\n        payload:\n          id: string\n\n", i, i))
	}
	sb.WriteString("\ndatabase:\n  tables:\n    - lookup_tbl\n\nroutes:\n")
	for i := 0; i < 50; i++ {
		sb.WriteString(fmt.Sprintf("  - on: EVT_%d\n    steps:\n      - action: db.insert\n        table: lookup_tbl\n\n", i))
	}

	path := filepath.Join(dir, "reg.spine")
	os.WriteFile(path, []byte(sb.String()), 0644)

	schema, err := manifest.ParseManifest(path)
	if err != nil {
		b.Fatal(err)
	}
	reg := manifest.NewRegistry(schema)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("EVT_%d", i%50)
		reg.GetRoutes(key)
	}
}

// ============================================================
// E2E Latency & Percentile Benchmarks (WAL fsync + HTTP)
// ============================================================

// BenchmarkEmitE2ELatency measures end-to-end emit latency including DB write and reports p50/p95/p99.
func BenchmarkEmitE2ELatency(b *testing.B) {
	dir := b.TempDir()
	content := `spine_version: 1
database:
  tables:
    - latency_table
nodes:
  LatNode:
    emits:
      - event: LAT_EVENT
        payload:
          data: string
routes:
  - on: LAT_EVENT
    steps:
      - action: db.insert
        table: latency_table
`
	mPath := filepath.Join(dir, "lat.spine")
	os.WriteFile(mPath, []byte(content), 0644)
	dbPath := filepath.Join(dir, "lat.db")

	eng, err := spine.NewFromFile(mPath, dbPath)
	if err != nil {
		b.Fatal(err)
	}
	defer eng.Close()

	payload := map[string]interface{}{"data": "latency_sample"}
	latencies := make([]time.Duration, 0, b.N)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		_, err := eng.Bus.Emit("LAT_EVENT", payload)
		dur := time.Since(start)
		if err != nil {
			b.Fatal(err)
		}
		latencies = append(latencies, dur)
	}
	b.StopTimer()

	if len(latencies) > 0 {
		sortDurations(latencies)
		p50 := latencies[len(latencies)*50/100]
		p95 := latencies[len(latencies)*95/100]
		p99 := latencies[len(latencies)*99/100]

		b.ReportMetric(float64(p50.Nanoseconds())/1e6, "p50_ms")
		b.ReportMetric(float64(p95.Nanoseconds())/1e6, "p95_ms")
		b.ReportMetric(float64(p99.Nanoseconds())/1e6, "p99_ms")
	}
}

func sortDurations(d []time.Duration) {
	for i := 0; i < len(d); i++ {
		for j := i + 1; j < len(d); j++ {
			if d[i] > d[j] {
				d[i], d[j] = d[j], d[i]
			}
		}
	}
}

