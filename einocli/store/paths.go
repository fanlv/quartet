package store

import "path/filepath"

// Path helpers for the eino-cli on-disk session layout. They replace the
// types/path functions used by the quartet repository original; the eino-cli
// session dir lives under ~/.eino/sessions/<sessionID>/ and is owned
// entirely by eino-cli.

const (
	metaDirName      = ".meta"
	messagesFileName = "messages.jsonl"
	summaryFileName  = "summary.json"
	tasksDirName     = ".tasks"
	reductionDirName = "reduction"
)

// MetaDir returns the .meta directory within a session directory.
func MetaDir(sessionDir string) string {
	return filepath.Join(sessionDir, metaDirName)
}

// MessagesFilePath returns the messages.jsonl file path within a session
// directory.
func MessagesFilePath(sessionDir string) string {
	return filepath.Join(sessionDir, metaDirName, messagesFileName)
}

// SummaryFilePath returns the summary.json file path within a session
// directory.
func SummaryFilePath(sessionDir string) string {
	return filepath.Join(sessionDir, metaDirName, summaryFileName)
}

// TasksDir returns the .tasks directory within a session directory.
func TasksDir(sessionDir string) string {
	return filepath.Join(sessionDir, tasksDirName)
}

// ReductionDir returns the reduction directory within a session directory.
func ReductionDir(sessionDir string) string {
	return filepath.Join(sessionDir, reductionDirName)
}
