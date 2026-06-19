package graph

import "testing"

func TestSubstituteVariables(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		vars     map[string]string
		disabled map[string]struct{}
		want     string
	}{
		{
			name: "known replaced",
			text: "hello {{name}}",
			vars: map[string]string{"name": "world"},
			want: "hello world",
		},
		{
			name: "unknown kept literal",
			text: "hello {{missing}}",
			vars: map[string]string{"name": "world"},
			want: "hello {{missing}}",
		},
		{
			name:     "disabled becomes empty",
			text:     "x={{a}}.",
			vars:     map[string]string{"a": "VAL"},
			disabled: disabledSet("a"),
			want:     "x=.",
		},
		{
			name:     "disabled with no entry becomes empty",
			text:     "x={{a}}.",
			disabled: disabledSet("a"),
			want:     "x=.",
		},
		{
			name: "no vars no disabled returns text",
			text: "literal {{a}}",
			want: "literal {{a}}",
		},
		{
			name: "no second pass substitution",
			// a's value contains {{b}}; it must NOT be re-substituted.
			text: "{{a}}",
			vars: map[string]string{"a": "{{b}}", "b": "DEEP"},
			want: "{{b}}",
		},
		{
			name: "multiple occurrences",
			text: "{{a}}-{{a}}-{{b}}",
			vars: map[string]string{"a": "1", "b": "2"},
			want: "1-1-2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := substituteVariables(tt.text, tt.vars, tt.disabled)
			if got != tt.want {
				t.Fatalf("substituteVariables() = %q, want %q", got, tt.want)
			}
		})
	}
}
