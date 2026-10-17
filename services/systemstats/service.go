package systemstats

import (
	"context"
	"fmt"
	"math"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"golang.org/x/sync/singleflight"

	"github.com/fanlv/quartet/types/model"
	quartetpath "github.com/fanlv/quartet/types/path"
)

const cacheLifetime = 3 * time.Second

// Service shares short-lived, on-demand samples between all statistics viewers.
// It does no background work when there are no viewers.
type Service struct {
	rootCtx context.Context
	mu      sync.Mutex
	cached  *model.SystemStats
	flight  singleflight.Group
}

func NewService(ctx context.Context) *Service {
	return &Service{rootCtx: ctx}
}

func (s *Service) Get(ctx context.Context) (*model.SystemStats, error) {
	if cached := s.snapshot(); cached != nil {
		return cached, nil
	}
	result := s.flight.DoChan("sample", func() (any, error) {
		if cached := s.snapshot(); cached != nil {
			return cached, nil
		}
		// A viewer disconnecting must not cancel the sample for other viewers.
		sampleCtx, cancel := context.WithTimeout(s.rootCtx, 5*time.Second)
		defer cancel()
		sample := s.collect(sampleCtx)
		if err := sampleCtx.Err(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.cached = sample
		s.mu.Unlock()
		return sample, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-result:
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Val.(*model.SystemStats), nil
	}
}

func (s *Service) snapshot() *model.SystemStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && time.Since(s.cached.SampledAt) < cacheLifetime {
		return s.cached
	}
	return nil
}

func (s *Service) collect(ctx context.Context) *model.SystemStats {
	report := &model.SystemStats{Errors: []string{}}
	// Measure CPU over a real interval, rather than reporting load average or
	// a process-local percentage. Other readings happen during this interval.
	var cpuUsage []float64
	var cpuErr error
	var pending sync.WaitGroup
	pending.Add(1)
	go func() {
		defer pending.Done()
		cpuUsage, cpuErr = cpu.PercentWithContext(ctx, time.Second, false)
	}()

	var err error
	report.Hostname, err = os.Hostname()
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("read hostname: %v", err))
	}

	var cpuModel string
	info, err := cpu.InfoWithContext(ctx)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("read CPU information: %v", err))
	} else if len(info) > 0 {
		cpuModel = info[0].ModelName
	}

	memory, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("read memory usage: %v", err))
	} else if memory.Total == 0 {
		report.Errors = append(report.Errors, "read memory usage: total memory is zero")
	} else {
		available := min(memory.Available, memory.Total)
		used := memory.Total - available
		report.Memory = &model.SystemMemoryStats{
			TotalBytes: memory.Total, UsedBytes: used, AvailableBytes: available,
			UsagePercent: 100 * float64(used) / float64(memory.Total),
		}
	}

	dataPath, err := quartetpath.LocalMemoryDir()
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("resolve data disk: %v", err))
	} else if usage, err := disk.UsageWithContext(ctx, dataPath); err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("read disk usage for %q: %v", dataPath, err))
	} else if usage.Total == 0 {
		report.Errors = append(report.Errors, fmt.Sprintf("read disk usage for %q: total capacity is zero", dataPath))
	} else {
		// Include reserved/unavailable space and, on APFS, sibling volumes in
		// occupied capacity so the percentage agrees with the free space shown.
		available := min(usage.Free, usage.Total)
		used := usage.Total - available
		report.Disk = &model.SystemDiskStats{
			Path: dataPath, TotalBytes: usage.Total, UsedBytes: used, AvailableBytes: available,
			UsagePercent: 100 * float64(used) / float64(usage.Total),
		}
	}

	pending.Wait()
	if cpuErr != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("read CPU usage: %v", cpuErr))
	} else if len(cpuUsage) != 1 || math.IsNaN(cpuUsage[0]) || math.IsInf(cpuUsage[0], 0) {
		report.Errors = append(report.Errors, fmt.Sprintf("read CPU usage: invalid sample %v", cpuUsage))
	} else {
		report.CPU = &model.SystemCPUStats{
			Model: cpuModel, LogicalCores: runtime.NumCPU(),
			UsagePercent: math.Max(0, math.Min(100, cpuUsage[0])),
		}
	}
	report.SampledAt = time.Now().UTC()
	return report
}
