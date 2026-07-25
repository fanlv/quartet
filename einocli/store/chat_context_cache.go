package store

import (
	"container/list"
	"os"
	"strconv"
	"sync"

	"github.com/cloudwego/eino/schema"
)

// messagesCache is a process-wide, byte-bounded LRU over the parsed contents of
// each session's messages.jsonl.
//
// Why this exists: GetSessionMessages (and the web reload path generally) reads
// a session's full message history on every request, and a long graph/loop node
// can accumulate multi-MB messages.jsonl files. Re-reading and re-Unmarshalling
// the whole file on every tab switch / SSE reconcile dominated the load time of
// a busy job. Caching the parsed slice keyed by the file's (size, mtime) makes
// the second and later reads of an unchanged session effectively free.
//
// Correctness model:
//   - Entries are validated against a fresh FileStat (size + mod-time) on every
//     read; any mismatch is a miss, so an out-of-band file change can never be
//     served stale.
//   - Write paths (append / replace / placeholder-stitch) invalidate the entry
//     explicitly after writing, which is belt-and-suspenders over the stat check
//     (local-fs mtime has 1s granularity, so two sub-second writes that net to
//     the same size could otherwise alias).
//   - All cache access happens while the caller holds the per-session
//     ctxRWMutex (read lock for lookups, write lock for invalidation), so a
//     concurrent write cannot tear a read. The cache's own mutex only guards the
//     shared LRU structure.
//
// The cached slice is returned by reference. Callers (loadAllMessagesLocked's
// consumers) treat the history as read-only — the handler projects into fresh
// model.HistoryMessage values and never mutates the schema.Message pointers — so
// sharing the backing slice across requests is safe.
type messagesCache struct {
	mu       sync.Mutex
	ll       *list.List // front = most-recently-used
	byKey    map[string]*list.Element
	curBytes int64
	maxBytes int64 // 0 disables caching entirely
}

type messagesCacheEntry struct {
	key   string
	size  int64
	mtime int64
	msgs  []*schema.Message
	bytes int64 // approximate retained size, for the byte budget
}

// messagesCacheBytesEnv overrides the cache's total byte budget. Set to 0 to
// disable the cache (every read goes to disk, restoring pre-cache behaviour).
const messagesCacheBytesEnv = "EINO_CLI_MESSAGES_CACHE_BYTES"

// defaultMessagesCacheBytes caps total retained message bytes. quartet is a
// single-user local process, so a few dozen MB of hot conversation history is a
// reasonable ceiling; beyond it the LRU evicts the coldest sessions.
const defaultMessagesCacheBytes = 64 << 20 // 64 MiB

var globalMessagesCache = newMessagesCache(messagesCacheBudget())

func messagesCacheBudget() int64 {
	raw := os.Getenv(messagesCacheBytesEnv)
	if raw == "" {
		return defaultMessagesCacheBytes
	}
	// A explicit 0 disables the cache; any other non-negative value sets the
	// budget. Negative / unparseable falls back to the default.
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return defaultMessagesCacheBytes
	}
	return v
}

func newMessagesCache(maxBytes int64) *messagesCache {
	return &messagesCache{
		ll:       list.New(),
		byKey:    make(map[string]*list.Element),
		maxBytes: maxBytes,
	}
}

// get returns the cached messages for key iff the entry exists and its recorded
// (size, mtime) match the freshly-stat'd file. A returned slice is shared, not
// copied — see the type doc for why that is safe.
func (c *messagesCache) get(key string, size, mtime int64) ([]*schema.Message, bool) {
	if c.maxBytes == 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byKey[key]
	if !ok {
		return nil, false
	}
	ent := el.Value.(*messagesCacheEntry)
	if ent.size != size || ent.mtime != mtime {
		// Stale: drop it so a subsequent put refreshes cleanly.
		c.removeElement(el)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return ent.msgs, true
}

// put stores (or refreshes) the parsed messages for key under the recorded file
// signature, evicting least-recently-used entries to stay within the budget.
func (c *messagesCache) put(key string, size, mtime int64, msgs []*schema.Message, bytes int64) {
	if c.maxBytes == 0 {
		return
	}
	// A single session larger than the whole budget is never worth caching (it
	// would evict everything else and still not fit).
	if bytes > c.maxBytes {
		c.invalidate(key)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byKey[key]; ok {
		ent := el.Value.(*messagesCacheEntry)
		c.curBytes -= ent.bytes
		ent.size, ent.mtime, ent.msgs, ent.bytes = size, mtime, msgs, bytes
		c.curBytes += bytes
		c.ll.MoveToFront(el)
	} else {
		ent := &messagesCacheEntry{key: key, size: size, mtime: mtime, msgs: msgs, bytes: bytes}
		c.byKey[key] = c.ll.PushFront(ent)
		c.curBytes += bytes
	}
	for c.curBytes > c.maxBytes {
		back := c.ll.Back()
		if back == nil {
			break
		}
		c.removeElement(back)
	}
}

// invalidate drops any entry for key. Called from write paths after the file
// changes.
func (c *messagesCache) invalidate(key string) {
	if c.maxBytes == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byKey[key]; ok {
		c.removeElement(el)
	}
}

// removeElement unlinks el from both the list and the index and reclaims its
// byte budget. Caller must hold c.mu.
func (c *messagesCache) removeElement(el *list.Element) {
	ent := el.Value.(*messagesCacheEntry)
	c.ll.Remove(el)
	delete(c.byKey, ent.key)
	c.curBytes -= ent.bytes
}

// estimateMessagesBytes approximates the retained heap size of a parsed message
// slice for the byte budget. It does not need to be exact — it only has to
// track gross magnitude so a few huge sessions don't blow the cache. We bill
// each message its content length plus a flat per-message overhead covering the
// struct, role, ids, and tool-call metadata.
func estimateMessagesBytes(msgs []*schema.Message) int64 {
	const perMessageOverhead = 256
	var total int64
	for _, m := range msgs {
		if m == nil {
			continue
		}
		total += int64(len(m.Content)) + perMessageOverhead
		for _, tc := range m.ToolCalls {
			total += int64(len(tc.Function.Arguments) + len(tc.Function.Name))
		}
	}
	return total
}
