package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fanlv/quartet/types/model"
)

// readConfigInput reads a GraphConfig JSON document from a file (when path is
// non-empty) or from stdin (when path is empty or "-"). The document may be
// either a bare GraphConfig ({"nodes":...,"edges":...}) or a wrapper object
// {"config": {...}} — the latter lets a caller pass the same JSON they'd give
// to create. Returns the parsed GraphConfig.
func readConfigInput(path string) (*model.GraphConfig, error) {
	raw, err := readRawInput(path)
	if err != nil {
		return nil, err
	}

	// Try the wrapper form first so a {"config": {...}, "name": ...} document is
	// accepted, then fall back to a bare GraphConfig.
	var wrapper struct {
		Config *model.GraphConfig `json:"config"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && wrapper.Config != nil {
		return wrapper.Config, nil
	}

	var cfg model.GraphConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config JSON: %w", err)
	}
	return &cfg, nil
}

// readRawInput returns the bytes from path, or stdin when path is "" or "-".
func readRawInput(path string) ([]byte, error) {
	if path == "" || path == "-" {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		if len(strings.TrimSpace(string(raw))) == 0 {
			return nil, fmt.Errorf("no input on stdin; pass --config-file or pipe JSON")
		}
		return raw, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file %q: %w", path, err)
	}
	return raw, nil
}

// printJSON writes v as indented JSON to stdout.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// printRawJSON writes an already-encoded JSON document to stdout, indented.
// Used for responses the CLI passes through verbatim instead of re-modeling
// (e.g. job get, whose envelope flattens Job fields next to lastEventSeq).
func printRawJSON(raw []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return fmt.Errorf("pretty-print response JSON: %w (body: %s)", err, strings.TrimSpace(string(raw)))
	}
	buf.WriteByte('\n')
	_, err := os.Stdout.Write(buf.Bytes())
	return err
}

// formatValidationErrors renders located validation errors one per line, each
// keeping its node/edge/variable/config-key context so the full reason is
// visible to the caller.
func formatValidationErrors(errs []model.GraphValidationError) string {
	var b strings.Builder
	for i, e := range errs {
		if i > 0 {
			b.WriteByte('\n')
		}
		var loc []string
		if e.NodeID != "" {
			loc = append(loc, "node="+e.NodeID)
		}
		if e.EdgeID != "" {
			loc = append(loc, "edge="+e.EdgeID)
		}
		if e.Variable != "" {
			loc = append(loc, "var="+e.Variable)
		}
		if e.ConfigKey != "" {
			loc = append(loc, "config="+e.ConfigKey)
		}
		b.WriteString("  - [")
		b.WriteString(string(e.Type))
		b.WriteByte(']')
		if len(loc) > 0 {
			b.WriteString(" (")
			b.WriteString(strings.Join(loc, ", "))
			b.WriteByte(')')
		}
		b.WriteByte(' ')
		b.WriteString(e.Message)
	}
	return b.String()
}
