// Command migrate-shell-script is a one-off migration tool: it inlines the
// content of scripts referenced by scriptId in loop configs (jobs, templates,
// schedules) into the step's message field, then clears scriptId/scriptName.
//
// It operates on raw JSON so it works both before and after the code change
// that removes the ScriptID/ScriptName fields, and never drops unknown fields.
//
// Usage: LOCAL_MEMORY=/path/to/memory go run ./cmd/migrate-shell-script
//
// Modified files are backed up to <file>.bak before being rewritten. Files
// containing a scriptId that cannot be resolved are reported and skipped
// (not rewritten). The agent/scripts/ directory itself is left untouched;
// delete it manually after verifying the migration.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type scriptFile struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

type stats struct {
	scanned  int
	modified int
	skipped  int
}

func main() {
	localMemory := os.Getenv("LOCAL_MEMORY")
	if localMemory == "" {
		fmt.Fprintln(os.Stderr, "LOCAL_MEMORY env var is not set")
		os.Exit(1)
	}

	scripts, err := loadScripts(filepath.Join(localMemory, "agent", "scripts"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "load scripts failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("loaded %d scripts\n", len(scripts))

	var st stats

	// Templates: {LOCAL_MEMORY}/agent/templates/*.json, loop config under "config".
	migrateGlob(filepath.Join(localMemory, "agent", "templates", "*.json"), "config", true, scripts, &st)

	// Schedules: {LOCAL_MEMORY}/agent/schedules/*.json, loop config under "loopConfig".
	migrateGlob(filepath.Join(localMemory, "agent", "schedules", "*.json"), "loopConfig", true, scripts, &st)

	// Jobs: {LOCAL_MEMORY}/workspaces/*/jobs/*/.meta/job.json, loop config under
	// "loopConfig". Jobs are persisted compact (json.Marshal), so write compact.
	migrateGlob(filepath.Join(localMemory, "workspaces", "*", "jobs", "*", ".meta", "job.json"), "loopConfig", false, scripts, &st)

	fmt.Printf("\ndone: scanned=%d modified=%d skipped=%d\n", st.scanned, st.modified, st.skipped)
	if st.skipped > 0 {
		fmt.Println("some files were skipped due to unresolved scriptIds; fix or remove those references and re-run")
		os.Exit(1)
	}
}

func loadScripts(dir string) (map[string]scriptFile, error) {
	scripts := make(map[string]scriptFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("scripts dir %s does not exist; nothing to load\n", dir)
			return scripts, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		var s scriptFile
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		if s.ID == "" {
			return nil, fmt.Errorf("script file %s has empty id", p)
		}
		scripts[s.ID] = s
	}
	return scripts, nil
}

func migrateGlob(pattern, configKey string, indent bool, scripts map[string]scriptFile, st *stats) {
	files, err := filepath.Glob(pattern)
	if err != nil {
		fmt.Fprintf(os.Stderr, "glob %s failed: %v\n", pattern, err)
		os.Exit(1)
	}
	for _, f := range files {
		st.scanned++
		modified, err := migrateFile(f, configKey, indent, scripts)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "SKIP %s: %v\n", f, err)
			st.skipped++
		case modified:
			fmt.Printf("migrated %s\n", f)
			st.modified++
		}
	}
}

func migrateFile(path, configKey string, indent bool, scripts map[string]scriptFile) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read: %w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return false, fmt.Errorf("parse: %w", err)
	}

	cfg, ok := doc[configKey].(map[string]any)
	if !ok {
		return false, nil // no loop config in this file
	}

	modified := false
	if flow, ok := cfg["flow"].([]any); ok {
		m, err := migrateNodes(flow, scripts)
		if err != nil {
			return false, err
		}
		modified = modified || m
	}
	if rounds, ok := cfg["rounds"].([]any); ok {
		m, err := migrateNodes(rounds, scripts)
		if err != nil {
			return false, err
		}
		modified = modified || m
	}
	if !modified {
		return false, nil
	}

	var out []byte
	if indent {
		out, err = json.MarshalIndent(doc, "", "  ")
	} else {
		out, err = json.Marshal(doc)
	}
	if err != nil {
		return false, fmt.Errorf("marshal: %w", err)
	}

	if err := os.WriteFile(path+".bak", data, 0644); err != nil {
		return false, fmt.Errorf("write backup: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		return false, fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, fmt.Errorf("rename: %w", err)
	}
	return true, nil
}

// migrateNodes walks flow nodes (recursing into children) or legacy rounds.
// For every entry with a non-empty scriptId it inlines the script content into
// message, moves scriptName into an empty label, and removes both fields.
func migrateNodes(nodes []any, scripts map[string]scriptFile) (bool, error) {
	modified := false
	for _, raw := range nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if children, ok := node["children"].([]any); ok {
			m, err := migrateNodes(children, scripts)
			if err != nil {
				return false, err
			}
			modified = modified || m
		}

		scriptID, _ := node["scriptId"].(string)
		scriptName, _ := node["scriptName"].(string)
		if scriptID == "" {
			if scriptName != "" { // dangling name without id: just drop it
				delete(node, "scriptName")
				modified = true
			}
			continue
		}

		sc, ok := scripts[scriptID]
		if !ok {
			// The referenced script was deleted. If the node still carries
			// inline content (the legacy execution fallback), keep it and just
			// drop the dangling reference; otherwise fail so data isn't lost.
			nodeID, _ := node["id"].(string)
			if msg, _ := node["message"].(string); msg != "" {
				fmt.Printf("WARN node %q references unknown scriptId %q (scriptName=%q); keeping inline message\n", nodeID, scriptID, scriptName)
				delete(node, "scriptId")
				delete(node, "scriptName")
				modified = true
				continue
			}
			return false, fmt.Errorf("node %q references unknown scriptId %q (scriptName=%q) and has no inline message", nodeID, scriptID, scriptName)
		}

		node["message"] = sc.Content
		if label, _ := node["label"].(string); label == "" && scriptName != "" {
			node["label"] = scriptName
		}
		delete(node, "scriptId")
		delete(node, "scriptName")
		modified = true
	}
	return modified, nil
}
