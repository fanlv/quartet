package model

import "time"

// SystemStats describes the machine running the Quartet backend. Disk usage
// refers to the filesystem containing LOCAL_MEMORY, including space used by
// other applications on that filesystem.
type SystemStats struct {
	Hostname  string             `json:"hostname"`
	SampledAt time.Time          `json:"sampledAt"`
	CPU       *SystemCPUStats    `json:"cpu"`
	Memory    *SystemMemoryStats `json:"memory"`
	Disk      *SystemDiskStats   `json:"disk"`
	Errors    []string           `json:"errors"`
}

type SystemCPUStats struct {
	Model        string  `json:"model"`
	LogicalCores int     `json:"logicalCores"`
	UsagePercent float64 `json:"usagePercent"`
}

type SystemMemoryStats struct {
	TotalBytes     uint64  `json:"totalBytes"`
	UsedBytes      uint64  `json:"usedBytes"`
	AvailableBytes uint64  `json:"availableBytes"`
	UsagePercent   float64 `json:"usagePercent"`
}

type SystemDiskStats struct {
	Path           string  `json:"path"`
	TotalBytes     uint64  `json:"totalBytes"`
	UsedBytes      uint64  `json:"usedBytes"`
	AvailableBytes uint64  `json:"availableBytes"`
	UsagePercent   float64 `json:"usagePercent"`
}
