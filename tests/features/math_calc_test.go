package features

// math.calc action tests — exercised black-box through a manifest (repo
// convention: engine behavior is pinned via public API + real SQLite).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	spine "github.com/AmritRai1234/spine"
	"github.com/AmritRai1234/spine/tests/testhelpers"
)

const mathCalcManifest = `spine_version: 1

database:
  tables:
    - calc_results

nodes:
  MathNode:
    emits:
      - event: CALC_OK
        payload:
          a: number
          b: number
      - event: CALC_INJECT
        payload:
          b: string
      - event: CALC_DIVZERO
        payload:
          a: number

routes:
  - on: CALC_OK
    steps:
      - action: math.calc
        set: sum
        expr: "$event.payload.a + $event.payload.b"
      - action: math.calc
        set: product
        expr: "$event.payload.a * $event.payload.b + 1"
      - action: math.calc
        set: weighted
        expr: "($event.payload.a + $event.payload.b) * 2"
      - action: db.insert
        table: calc_results

  - on: CALC_INJECT
    steps:
      - action: math.calc
        set: out
        expr: "1 + $event.payload.b"

  - on: CALC_DIVZERO
    steps:
      - action: math.calc
        set: out
        expr: "10 / $event.payload.a"
`

func newMathCalcEngine(t *testing.T) *spine.Engine {
	t.Helper()
	dir := t.TempDir()
	spineFile := filepath.Join(dir, "math.spine")
	if err := os.WriteFile(spineFile, []byte(mathCalcManifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	eng, err := spine.NewFromFile(spineFile, filepath.Join(dir, "math.db"))
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func calcResult(t *testing.T, eng *spine.Engine) (sum, product, weighted float64) {
	t.Helper()
	testhelpers.WaitUntil(t, "calc row", func() bool {
		var n int
		_ = eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM calc_results`).Scan(&n)
		return n >= 1
	})
	row := eng.Bus.DB().QueryRow(`SELECT sum, product, weighted FROM calc_results ORDER BY _spine_id DESC LIMIT 1`)
	if err := row.Scan(&sum, &product, &weighted); err != nil {
		t.Fatalf("scan calc row: %v", err)
	}
	return
}

// TestMathCalcComputesValues: precedence, parentheses, and payload variable
// resolution all evaluate correctly and persist through db.insert.
func TestMathCalcComputesValues(t *testing.T) {
	eng := newMathCalcEngine(t)

	if _, err := eng.Bus.Emit("CALC_OK", map[string]interface{}{"a": 2.0, "b": 3.0}); err != nil {
		t.Fatalf("calc emit failed: %v", err)
	}
	sum, product, weighted := calcResult(t, eng)
	if sum != 5.0 {
		t.Errorf("expected sum 2+3=5, got %v", sum)
	}
	if product != 7.0 {
		t.Errorf("expected product 2*3+1=7 (precedence), got %v", product)
	}
	if weighted != 10.0 {
		t.Errorf("expected weighted (2+3)*2=10 (parens), got %v", weighted)
	}
}

// TestMathCalcRejectsInjection: a payload value carrying SQL/extra operators
// must fail the step, not execute or silently truncate.
func TestMathCalcRejectsInjection(t *testing.T) {
	eng := newMathCalcEngine(t)

	_, err := eng.Bus.Emit("CALC_INJECT", map[string]interface{}{"b": "1; DROP TABLE calc_results"})
	if err == nil {
		t.Fatal("injected expression accepted")
	}
	if !strings.Contains(err.Error(), "must be a plain number") {
		t.Fatalf("expected operand rejection, got: %v", err)
	}

	// Regression: an expression-shaped payload value ("0 + 9999") used to be
	// spliced into the expression as text, letting a client forge computed
	// totals (expr "1 + $event.payload.b" with b="0 + 9999" computed 10000).
	// Payload values are operands, never expression text — this must fail.
	_, err = eng.Bus.Emit("CALC_INJECT", map[string]interface{}{"b": "0 + 9999"})
	if err == nil {
		t.Fatal("expression-shaped operand accepted")
	}
	if !strings.Contains(err.Error(), "must be a plain number") {
		t.Fatalf("expected operand rejection for '0 + 9999', got: %v", err)
	}

	// Table must still exist and be empty.
	var n int
	if err := eng.Bus.DB().QueryRow(`SELECT COUNT(*) FROM calc_results`).Scan(&n); err != nil {
		t.Fatalf("calc_results table damaged: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected no rows after injection attempt, got %d", n)
	}
}

// TestMathCalcRejectsDivisionByZero: hard error, not Inf/NaN persisted.
func TestMathCalcRejectsDivisionByZero(t *testing.T) {
	eng := newMathCalcEngine(t)

	_, err := eng.Bus.Emit("CALC_DIVZERO", map[string]interface{}{"a": 0.0})
	if err == nil {
		t.Fatal("division by zero accepted")
	}
	if !strings.Contains(err.Error(), "division by zero") {
		t.Fatalf("expected division by zero error, got: %v", err)
	}
}
