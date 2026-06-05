package fileserver

import (
	"path/filepath"
	"testing"

	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
)

func TestNewLocal_FileRoundTrip(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "note.txt")
	fs := NewLocal()
	if err := fs.FileWrite(&fsmodel.FileWriteRequest{File: file, Content: "hello"}); err != nil {
		t.Fatalf("FileWrite failed: %v", err)
	}
	read, err := fs.FileRead(&fsmodel.FileReadRequest{File: file})
	if err != nil {
		t.Fatalf("FileRead failed: %v", err)
	}
	if read.Content != "hello" {
		t.Fatalf("FileRead content = %q, want %q", read.Content, "hello")
	}
	stat, err := fs.FileStat(&fsmodel.FileStatRequest{Path: file})
	if err != nil {
		t.Fatalf("FileStat failed: %v", err)
	}
	if !stat.Exists || stat.IsDir {
		t.Fatalf("unexpected stat result: %#v", stat)
	}
	exists, err := fs.FileExists(file)
	if err != nil {
		t.Fatalf("FileExists failed: %v", err)
	}
	if !exists.Exists {
		t.Fatalf("FileExists should report true")
	}
}

func TestNewLocal_JSONLRoundTrip(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "history.jsonl")
	fs := NewLocal()
	if err := fs.JSONLAppendLine(&fsmodel.JSONLAppendRequest{File: file, JSONString: []string{`{"a":1}`, `{"b":2}`}}); err != nil {
		t.Fatalf("JSONLAppendLine failed: %v", err)
	}
	count, err := fs.JSONLCountLines(&fsmodel.JSONLCountRequest{File: file})
	if err != nil {
		t.Fatalf("JSONLCountLines failed: %v", err)
	}
	if count.Lines != 2 {
		t.Fatalf("JSONLCountLines = %d, want 2", count.Lines)
	}
	one := 1
	read, err := fs.JSONLReadLines(&fsmodel.JSONLReadRequest{File: file, StartLine: 1, Count: &one})
	if err != nil {
		t.Fatalf("JSONLReadLines failed: %v", err)
	}
	if len(read.Lines) != 1 || read.Lines[0] != `{"b":2}` {
		t.Fatalf("unexpected JSONLReadLines result: %#v", read)
	}
}
