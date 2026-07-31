package codegen

import (
	"strings"
	"testing"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

func TestMapFieldType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"string", "string"},
		{"str", "string"},
		{"TEXT", "string"},
		{"number", "number"},
		{"float", "number"},
		{"int", "number"},
		{"boolean", "boolean"},
		{"bool", "boolean"},
		{"unknown", "any"},
	}

	for _, tt := range tests {
		got := MapFieldType(tt.input)
		if got != tt.expected {
			t.Errorf("MapFieldType(%s) = %s, expected %s", tt.input, got, tt.expected)
		}
	}
}

func TestGenerateTypeScript(t *testing.T) {
	schema := &manifest.SpineSchema{
		SpineVersion: 1,
		Nodes: []manifest.Node{
			{
				Name: "AuthNode",
				Emits: []manifest.Emit{
					{
						Event: "USER_LOGIN",
						Fields: []manifest.PayloadField{
							{Name: "email", FieldType: "string"},
							{Name: "attempts", FieldType: "number"},
						},
					},
				},
				Listens: []manifest.Listen{
					{
						State: "USER_AUTHENTICATED",
						Fields: []manifest.PayloadField{
							{Name: "user_id", FieldType: "string"},
							{Name: "is_admin", FieldType: "boolean"},
						},
					},
				},
			},
		},
	}

	code := GenerateTypeScript(schema)

	if !strings.Contains(code, "export interface UserLoginPayload {") {
		t.Errorf("Generated TypeScript missing UserLoginPayload interface:\n%s", code)
	}
	if !strings.Contains(code, "email: string;") {
		t.Errorf("Generated TypeScript missing email field:\n%s", code)
	}
	if !strings.Contains(code, "attempts: number;") {
		t.Errorf("Generated TypeScript missing attempts field:\n%s", code)
	}
	if !strings.Contains(code, "export interface UserAuthenticatedState {") {
		t.Errorf("Generated TypeScript missing UserAuthenticatedState interface:\n%s", code)
	}
	if !strings.Contains(code, "is_admin: boolean;") {
		t.Errorf("Generated TypeScript missing is_admin boolean field:\n%s", code)
	}
	if !strings.Contains(code, "'USER_LOGIN': UserLoginPayload;") {
		t.Errorf("Generated TypeScript missing EventPayloadMap mapping:\n%s", code)
	}
}
