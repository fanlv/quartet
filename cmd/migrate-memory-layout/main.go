package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	var (
		root      string
		mode      string
		batchID   string
		reportOut string
	)
	flag.StringVar(&root, "root", os.Getenv("LOCAL_MEMORY"), "Memory root (defaults to LOCAL_MEMORY)")
	flag.StringVar(&mode, "mode", "plan", "plan, execute, or verify")
	flag.StringVar(&batchID, "batch-id", "", "stable migration batch identifier")
	flag.StringVar(&reportOut, "report", "", "JSON report path outside Memory")
	flag.Parse()

	root = strings.TrimSpace(root)
	if root == "" {
		fatalf("Memory root is required: pass --root or set LOCAL_MEMORY")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		fatalf("resolve Memory root %q failed: %v", root, err)
	}
	root = filepath.Clean(absRoot)
	if batchID == "" {
		batchID = "memory-layout-v1-" + time.Now().Format("20060102-150405")
	}
	if reportOut == "" {
		reportOut = filepath.Join(os.TempDir(), "quartet-"+batchID+".json")
	}

	m, err := newMigrator(root, batchID, reportOut)
	if err != nil {
		fatalf("initialize migration failed: %v", err)
	}

	switch mode {
	case "plan":
		err = m.plan()
	case "execute":
		err = m.execute()
	case "verify":
		err = m.verifyMode()
	default:
		err = fmt.Errorf("unsupported --mode %q: expected plan, execute, or verify", mode)
	}
	if reportErr := m.writeReport(mode, err); reportErr != nil {
		if err != nil {
			fatalf("%v\nwrite migration report failed: %v", err, reportErr)
		}
		fatalf("write migration report failed: %v", reportErr)
	}
	if err != nil {
		fatalf("%v\nreport: %s", err, reportOut)
	}
	fmt.Printf("memory layout %s succeeded\nreport: %s\n", mode, reportOut)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
