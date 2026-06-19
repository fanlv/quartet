package graph

import "testing"

func disabledSet(names ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[n] = struct{}{}
	}
	return m
}

func TestEvaluateCondition_StringComparison(t *testing.T) {
	tests := []struct {
		name string
		expr string
		vars map[string]string
		want bool
	}{
		{"eq true", `{{a}} == "x"`, map[string]string{"a": "x"}, true},
		{"eq false", `{{a}} == "x"`, map[string]string{"a": "y"}, false},
		{"neq true", `{{a}} != "x"`, map[string]string{"a": "y"}, true},
		{"literal vs literal", `"a" == "a"`, nil, true},
		// "10" > "9" is FALSE under code-point lexicographic order ('1' < '9').
		{"lexicographic gt", `{{a}} > "9"`, map[string]string{"a": "10"}, false},
		{"lexicographic lt", `{{a}} < "9"`, map[string]string{"a": "10"}, true},
		{"gte equal", `{{a}} >= "abc"`, map[string]string{"a": "abc"}, true},
		{"lte", `{{a}} <= "b"`, map[string]string{"a": "a"}, true},
		{"startwith true", `{{a}} StartWith "he"`, map[string]string{"a": "hello"}, true},
		{"startwith false", `{{a}} StartWith "lo"`, map[string]string{"a": "hello"}, false},
		{"endwith true", `{{a}} EndWith "lo"`, map[string]string{"a": "hello"}, true},
		{"endwith false", `{{a}} EndWith "he"`, map[string]string{"a": "hello"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EvaluateCondition(tt.expr, CondEvalInput{Variables: tt.vars})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("EvaluateCondition(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestEvaluateCondition_Options(t *testing.T) {
	tests := []struct {
		name string
		expr string
		vars map[string]string
		want bool
	}{
		{"ignore case", `{{a}} == "ABC" 忽略大小写`, map[string]string{"a": "abc"}, true},
		{"case sensitive default", `{{a}} == "ABC"`, map[string]string{"a": "abc"}, false},
		{"ignore space", `{{a}} == "a b c" 忽略空格`, map[string]string{"a": "abc"}, true},
		{"ignore both", `{{a}} == "A B C" 忽略空格 忽略大小写`, map[string]string{"a": "abc"}, true},
		{"ignore space startwith", `{{a}} StartWith " h e" 忽略空格`, map[string]string{"a": "hello"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EvaluateCondition(tt.expr, CondEvalInput{Variables: tt.vars})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("EvaluateCondition(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestEvaluateCondition_BooleanCombination(t *testing.T) {
	vars := map[string]string{"a": "1", "b": "2"}
	tests := []struct {
		expr string
		want bool
	}{
		{`{{a}} == "1" 且 {{b}} == "2"`, true},
		{`{{a}} == "1" 且 {{b}} == "x"`, false},
		{`{{a}} == "x" 或 {{b}} == "2"`, true},
		{`{{a}} == "x" 或 {{b}} == "x"`, false},
		{`非 {{a}} == "x"`, true},
		{`非 {{a}} == "1"`, false},
		// precedence: 且 binds tighter than 或
		{`{{a}} == "x" 或 {{a}} == "1" 且 {{b}} == "2"`, true},
		{`{{a}} == "x" 或 {{a}} == "1" 且 {{b}} == "x"`, false},
		// parentheses override
		{`({{a}} == "x" 或 {{a}} == "1") 且 {{b}} == "x"`, false},
		{`({{a}} == "x" 或 {{a}} == "1") 且 {{b}} == "2"`, true},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			got, err := EvaluateCondition(tt.expr, CondEvalInput{Variables: vars})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("EvaluateCondition(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestEvaluateCondition_DisabledIsEmptyString(t *testing.T) {
	// Disabled variable participates as empty string, taking precedence over
	// any stored value.
	in := CondEvalInput{
		Variables: map[string]string{"a": "nonempty"},
		Disabled:  disabledSet("a"),
	}
	got, err := EvaluateCondition(`{{a}} == ""`, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatalf("disabled variable should compare as empty string")
	}
}

func TestEvaluateCondition_UnknownVariableFails(t *testing.T) {
	_, err := EvaluateCondition(`{{missing}} == "x"`, CondEvalInput{Variables: map[string]string{"a": "1"}})
	if err == nil {
		t.Fatal("expected evaluation error for unknown variable")
	}
	if err.Var != "missing" || err.State != "unknown" {
		t.Fatalf("error = %+v, want Var=missing State=unknown", err)
	}
}

func TestEvaluateCondition_PrunedVariableFails(t *testing.T) {
	in := CondEvalInput{
		Variables: map[string]string{},
		Pruned:    disabledSet("p"),
	}
	_, err := EvaluateCondition(`{{p}} == "x"`, in)
	if err == nil {
		t.Fatal("expected evaluation error for pruned variable")
	}
	if err.State != "pruned" {
		t.Fatalf("error state = %q, want pruned", err.State)
	}
	if err.Expr == "" {
		t.Fatal("error should carry the expression text")
	}
}

func TestEvaluateCondition_LastAssistantWholeStringNotTruncated(t *testing.T) {
	// _last_assistant_msg participates as the whole string (multi-line, not
	// split/truncated).
	big := "line1\nline2\nline3 with = and spaces"
	in := CondEvalInput{Variables: map[string]string{reservedLastAssistant: big}}
	got, err := EvaluateCondition(`{{_last_assistant_msg}} EndWith "spaces"`, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatalf("expected EndWith over full multi-line value")
	}
}
