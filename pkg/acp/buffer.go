package acp

import (
	"bytes"
	"fmt"
	"sync"
)

// maxStderrSize is the maximum bytes kept in the stderr buffer per ACP connection.
// The buffer preserves both startup context (head) and the latest diagnostics
// (tail), so verbose agent subprocesses cannot hide the final crash reason.
const maxStderrSize = 10 << 20 // 10 MB

const (
	stderrHeadSize = 2 << 20 // 2 MB
	stderrTailSize = maxStderrSize - stderrHeadSize
)

// syncBuffer is a thread-safe, size-limited buffer for capturing subprocess stderr.
type syncBuffer struct {
	mu    sync.Mutex
	head  bytes.Buffer
	tail  []byte
	total int64
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	written := len(p)
	if written == 0 {
		return 0, nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.total += int64(written)

	if b.head.Len() < stderrHeadSize {
		headRemain := stderrHeadSize - b.head.Len()
		if headRemain > len(p) {
			headRemain = len(p)
		}
		_, _ = b.head.Write(p[:headRemain])
		p = p[headRemain:]
	}
	if len(p) > 0 {
		b.writeTailLocked(p)
	}
	return written, nil
}

func (b *syncBuffer) writeTailLocked(p []byte) {
	if len(p) >= stderrTailSize {
		b.tail = append(b.tail[:0], p[len(p)-stderrTailSize:]...)
		return
	}
	if len(b.tail)+len(p) > stderrTailSize {
		overflow := len(b.tail) + len(p) - stderrTailSize
		copy(b.tail, b.tail[overflow:])
		b.tail = b.tail[:len(b.tail)-overflow]
	}
	b.tail = append(b.tail, p...)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	head := b.head.String()
	if len(b.tail) == 0 {
		return head
	}

	omitted := b.total - int64(b.head.Len()) - int64(len(b.tail))
	if omitted <= 0 {
		return head + string(b.tail)
	}
	return head + fmt.Sprintf("\n[stderr omitted %d bytes; preserving first %d bytes and last %d bytes]\n",
		omitted, b.head.Len(), len(b.tail)) + string(b.tail)
}
