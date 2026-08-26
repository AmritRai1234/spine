package manifest

import "testing"

// Parser unit tests (the pkg/manifest package previously had zero test
// files — all coverage was black-box via tests/parser_test.go).

func TestUnquoteStripsOnlyDoubleQuotes(t *testing.T) {
	cases := []struct{ in, want string }{
		{`"value"`, "value"},
		{`  " padded "  `, " padded "},
		{`'single'`, `'single'`}, // single quotes are NOT stripped by unquote
		{`plain`, "plain"},
		{`""`, ""},
	}
	for _, tc := range cases {
		if got := unquote(tc.in); got != tc.want {
			t.Errorf("unquote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFieldTypeValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{"string", "string"},
		{"string # primary contact", "string"}, // inline comment stripped
		{"number # qty in units", "number"},
		{`"string"`, "string"},
		{"  text  ", "text"},
	}
	for _, tc := range cases {
		if got := fieldTypeValue(tc.in); got != tc.want {
			t.Errorf("fieldTypeValue(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseBoolFlag(t *testing.T) {
	ok := map[string]bool{
		"true": true, "True": true, "TRUE": true,
		"false": false, "False": false,
		"1": true, "0": false, "t": true, "f": false,
	}
	for in, want := range ok {
		got, err := parseBoolFlag("test.spine", 1, "read_only", in)
		if err != nil {
			t.Errorf("parseBoolFlag(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseBoolFlag(%q) = %v, want %v", in, got, want)
		}
	}

	for _, bad := range []string{"yes", "no", "on", "garbage", ""} {
		if _, err := parseBoolFlag("test.spine", 1, "read_only", bad); err == nil {
			t.Errorf("parseBoolFlag(%q) must error (fail-closed), got nil", bad)
		}
	}
}

func TestEqualEventShape(t *testing.T) {
	a := map[string]string{"email": "string", "age": "number"}
	b := map[string]string{"age": "number", "email": "string"} // order-insensitive
	c := map[string]string{"email": "string"}                  // missing field
	d := map[string]string{"email": "number"}                  // type conflict

	if !equalEventShape(a, b) {
		t.Error("equal shapes with different key order must be equal")
	}
	if equalEventShape(a, c) || equalEventShape(a, d) {
		t.Error("different shapes must not be equal")
	}
}
