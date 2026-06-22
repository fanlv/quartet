package graph

import (
	"encoding/base64"
	"testing"
)

func TestParseShellControl_Values(t *testing.T) {
	body := "B64:foo=" + base64.StdEncoding.EncodeToString([]byte("hello world")) + "\n" +
		"bar=plain\n" +
		"B64:empty=\n" + // empty base64 → empty value
		"B64:special=" + base64.StdEncoding.EncodeToString([]byte("a = b\tc")) + "\n" +
		"B64:foo=" + base64.StdEncoding.EncodeToString([]byte("last")) // last wins
	res, err := ParseShellControl(body, []string{"foo", "bar", "empty", "special"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{"foo": "last", "bar": "plain", "empty": "", "special": "a = b\tc"}
	for k, v := range want {
		if res.Variables[k] != v {
			t.Fatalf("var %q = %q, want %q", k, res.Variables[k], v)
		}
	}
}

func TestParseShellControl_Signals(t *testing.T) {
	res, err := ParseShellControl("STOP_WORKFLOW", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.StopWorkflow || res.StopLoop {
		t.Fatalf("StopWorkflow=%v StopLoop=%v, want true/false", res.StopWorkflow, res.StopLoop)
	}

	res, err = ParseShellControl("B64:v="+base64.StdEncoding.EncodeToString([]byte("x"))+"\nSTOP_LOOP\nx=1", []string{"v", "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.StopLoop {
		t.Fatal("expected StopLoop true")
	}
}

func TestParseShellControl_Errors(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		declared []string
		wantVar  string
	}{
		{"missing", "", []string{"need"}, "need"},
		{"invalid name", "1bad=1", []string{"foo"}, "1bad"},
		{"reserved", "_x=1", []string{"foo"}, "_x"},
		{"bad base64", "B64:foo=not!!base64", []string{"foo"}, "foo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseShellControl(tt.body, tt.declared)
			if err == nil {
				t.Fatal("expected control error")
			}
			if err.Variable != tt.wantVar {
				t.Fatalf("error variable = %q, want %q (msg=%s)", err.Variable, tt.wantVar, err.Message)
			}
		})
	}
}

func TestParseShellControl_NonKeyValueLineIgnored(t *testing.T) {
	// Diagnostic lines without '=' are ignored, not errors.
	res, err := ParseShellControl("just a log line\nx=1", []string{"x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Variables["x"] != "1" {
		t.Fatalf("x = %q, want 1", res.Variables["x"])
	}
}

func TestParseShellControl_UndeclaredFlowsThrough(t *testing.T) {
	// Permissive contract: a produced variable flows downstream whether or not
	// it was declared. Declared ones still must be produced (completeness).
	body := "extra=surplus\nB64:keep=" + base64.StdEncoding.EncodeToString([]byte("v"))
	res, err := ParseShellControl(body, []string{"keep"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Variables["keep"] != "v" {
		t.Fatalf("keep = %q, want v", res.Variables["keep"])
	}
	if res.Variables["extra"] != "surplus" {
		t.Fatalf("undeclared variable %q should flow through, got %q", "extra", res.Variables["extra"])
	}
}

func TestParseShellControl_NoDeclarationFlowsAll(t *testing.T) {
	// A Shell node with zero declared outputs still surfaces every quartet_set
	// variable to downstream nodes.
	body := "a=1\nB64:b=" + base64.StdEncoding.EncodeToString([]byte("two"))
	res, err := ParseShellControl(body, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Variables["a"] != "1" || res.Variables["b"] != "two" {
		t.Fatalf("got %+v, want a=1 b=two", res.Variables)
	}
}
