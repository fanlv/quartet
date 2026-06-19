package graph

import (
	"strings"
	"testing"
)

func TestParseCondition_Valid(t *testing.T) {
	cases := []string{
		`{{a}} == "1"`,
		`{{a}} != "1"`,
		`{{a}} > "9"`,
		`{{a}} >= "9"`,
		`{{a}} < "9"`,
		`{{a}} <= "9"`,
		`{{name}} StartWith "foo"`,
		`{{name}} EndWith "bar"`,
		`{{a}} == "1" 且 {{b}} == "2"`,
		`{{a}} == "1" 或 {{b}} == "2"`,
		`非 {{a}} == "1"`,
		`非 非 {{a}} == "1"`,
		`( {{a}} == "1" 或 {{b}} == "2" ) 且 {{c}} == "3"`,
		`{{a}} == "1" 忽略大小写`,
		`{{a}} == "1" 忽略空格`,
		`{{a}} == "1" 忽略大小写 忽略空格`,
		`{{a}} == "with \"quote\" and \\ backslash"`,
		`"literal" == {{a}}`,
		`{{flag}} == "true"`,
		// whitespace insensitivity outside strings
		`{{a}}=="1"且{{b}}=="2"`,
		// nested parens
		`((({{a}} == "1")))`,
	}
	for _, expr := range cases {
		if _, err := ParseCondition(expr); err != nil {
			t.Errorf("ParseCondition(%q) unexpected error: %v", expr, err)
		}
	}
}

func TestParseCondition_Invalid(t *testing.T) {
	cases := []struct {
		expr   string
		reason string
	}{
		{`{{a}}`, "bare variable truth test"},
		{`{{a}} == "1" 且`, "trailing operator"},
		{`( {{a}} == "1"`, "unclosed paren"},
		{`{{a}} == "1" )`, "extra close paren"},
		{`{{a}} == "unterminated`, "unterminated string"},
		{`{{a}} == "bad \q escape"`, "illegal escape"},
		{`{{a}} = "1"`, "single equals is not an operator"},
		{`{{a}} ~ "1"`, "unknown operator token"},
		{`{{a}} Contains "1"`, "unknown word operator"},
		{`{{a}} == {{`, "unterminated variable"},
		{`{{1bad}} == "1"`, "invalid variable name"},
		{`"x" "y"`, "missing operator between operands"},
		{`且 {{a}} == "1"`, "leading binary operator"},
		{``, "empty expression"},
		{`{{a}} == "1" "2"`, "dangling operand"},
		{`{a}} == "1"`, "single brace open"},
	}
	for _, tc := range cases {
		if _, err := ParseCondition(tc.expr); err == nil {
			t.Errorf("ParseCondition(%q) expected error (%s), got nil", tc.expr, tc.reason)
		}
	}
}

func TestParseCondition_Options(t *testing.T) {
	node, err := ParseCondition(`{{a}} == "1" 忽略大小写 忽略空格`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cmp, ok := node.(*CondCompare)
	if !ok {
		t.Fatalf("expected *CondCompare, got %T", node)
	}
	if !cmp.IgnoreCase || !cmp.IgnoreSpace {
		t.Errorf("expected both ignore options set, got case=%v space=%v", cmp.IgnoreCase, cmp.IgnoreSpace)
	}
	if !cmp.Left.IsVar || cmp.Left.Var != "a" {
		t.Errorf("left operand mismatch: %+v", cmp.Left)
	}
	if cmp.Right.IsVar || cmp.Right.Lit != "1" {
		t.Errorf("right operand mismatch: %+v", cmp.Right)
	}
}

func TestParseCondition_Precedence(t *testing.T) {
	// 且 binds tighter than 或: a 或 b 且 c == a 或 (b 且 c)
	node, err := ParseCondition(`{{a}} == "1" 或 {{b}} == "2" 且 {{c}} == "3"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	top, ok := node.(*CondBinary)
	if !ok || top.Op != "或" {
		t.Fatalf("expected top-level 或 node, got %T %+v", node, node)
	}
	right, ok := top.Right.(*CondBinary)
	if !ok || right.Op != "且" {
		t.Fatalf("expected right child to be 且 node, got %T", top.Right)
	}
}

func TestParseCondition_EscapeValue(t *testing.T) {
	node, err := ParseCondition(`{{a}} == "x\"y\\z"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cmp := node.(*CondCompare)
	if cmp.Right.Lit != `x"y\z` {
		t.Errorf("escape unescaping wrong: got %q", cmp.Right.Lit)
	}
}

func TestParseCondition_MultilineStringRejected(t *testing.T) {
	if _, err := ParseCondition("{{a}} == \"line1\nline2\""); err == nil {
		t.Error("expected error for multi-line string literal")
	} else if !strings.Contains(err.Error(), "multiple lines") {
		t.Errorf("expected multi-line error, got: %v", err)
	}
}
