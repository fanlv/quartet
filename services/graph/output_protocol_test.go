package graph

import (
	"strings"
	"testing"
)

func TestParseQuartetOutput_Success(t *testing.T) {
	raw := strings.Join([]string{
		"some reasoning here",
		"QUARTET_OUTPUT:foo=bar",
		"  \tQUARTET_OUTPUT:baz=hello world", // leading whitespace ignored
		"QUARTET_OUTPUT:empty=",              // empty value allowed
		"QUARTET_OUTPUT:eq=a=b=c",            // first '=' split, value keeps '='
	}, "\n")
	res, err := ParseQuartetOutput(raw, []string{"foo", "baz", "empty", "eq"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{"foo": "bar", "baz": "hello world", "empty": "", "eq": "a=b=c"}
	for k, v := range want {
		if res.Variables[k] != v {
			t.Fatalf("var %q = %q, want %q", k, res.Variables[k], v)
		}
	}
}

func TestParseQuartetOutput_SubstringMatch(t *testing.T) {
	// The marker is matched anywhere within a line: a non-whitespace prefix no
	// longer disqualifies it, so a marker glued onto preceding text is still
	// recognized (e.g. two assistant messages concatenated without a newline).
	raw := "2QUARTET_OUTPUT:answer=2"
	res, err := ParseQuartetOutput(raw, []string{"answer"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Variables["answer"] != "2" {
		t.Fatalf("answer = %q, want 2", res.Variables["answer"])
	}
}

func TestParseQuartetOutput_LastWins(t *testing.T) {
	raw := "QUARTET_OUTPUT:foo=first\nQUARTET_OUTPUT:foo=second"
	res, err := ParseQuartetOutput(raw, []string{"foo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Variables["foo"] != "second" {
		t.Fatalf("foo = %q, want second", res.Variables["foo"])
	}
}

func TestParseQuartetOutput_Errors(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		declared []string
		wantVar  string
	}{
		{"undeclared", "QUARTET_OUTPUT:foo=1\nQUARTET_OUTPUT:extra=2", []string{"foo"}, "extra"},
		{"missing", "QUARTET_OUTPUT:foo=1", []string{"foo", "bar"}, "bar"},
		{"invalid name", "QUARTET_OUTPUT:1bad=1", []string{"foo"}, "1bad"},
		{"reserved name", "QUARTET_OUTPUT:_secret=1", []string{"foo"}, "_secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseQuartetOutput(tt.raw, tt.declared)
			if err == nil {
				t.Fatal("expected protocol error")
			}
			if err.Variable != tt.wantVar {
				t.Fatalf("error variable = %q, want %q (msg=%s)", err.Variable, tt.wantVar, err.Message)
			}
		})
	}
}

func TestParseQuartetOutput_NoDeclaredNoOutput(t *testing.T) {
	res, err := ParseQuartetOutput("just text, no markers", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Variables) != 0 {
		t.Fatalf("expected no variables, got %v", res.Variables)
	}
}

func TestBuildOutputProtocolSuffix(t *testing.T) {
	if buildOutputProtocolSuffix(nil) != "" {
		t.Fatal("no declared vars should yield empty suffix")
	}
	suffix := buildOutputProtocolSuffix([]string{"a", "b"})
	if !strings.Contains(suffix, "QUARTET_OUTPUT:a=") || !strings.Contains(suffix, "QUARTET_OUTPUT:b=") {
		t.Fatalf("suffix missing per-variable lines: %q", suffix)
	}
}

func TestBuildEvaluatorPrompt(t *testing.T) {
	p := buildEvaluatorPrompt("是否完成", []string{"decision"})
	if !strings.Contains(p, "是否完成") {
		t.Fatal("evaluator prompt must embed the user condition")
	}
	if !strings.Contains(p, "QUARTET_OUTPUT:decision=") {
		t.Fatal("evaluator prompt must include output protocol for declared variable")
	}
	if strings.Contains(p, "LOOP_DECISION") {
		t.Fatal("evaluator prompt must not use the legacy LOOP_DECISION marker")
	}
}
