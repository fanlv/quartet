package model

import "time"

const (
	CurrentMemoryLayoutVersion = 1
	MemoryLayoutStatusComplete = "complete"
)

// MemoryLayoutManifest marks a fully activated, versioned Memory layout.
// Quartet refuses to start while the marker is absent, incomplete or newer
// than the running binary.
type MemoryLayoutManifest struct {
	Version     int       `json:"version"`
	Status      string    `json:"status"`
	BatchID     string    `json:"batchId"`
	CompletedAt time.Time `json:"completedAt"`
}
