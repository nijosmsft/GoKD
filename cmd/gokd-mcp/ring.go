package main

import (
	"container/list"
	"sync"
)

// ring is a small generic ring buffer keyed by monotonic uint64 sequence
// numbers. Push assigns the next sequence and may overwrite the oldest
// entry; Since returns all entries strictly after token, plus a "dropped"
// count for entries that would have followed token but were evicted before
// the caller asked.
//
// Sequence numbers start at 1; token 0 is the conventional "I have seen
// nothing yet" sentinel that returns every live entry.
//
// All operations are safe for concurrent use.
type ring[T any] struct {
	mu       sync.Mutex
	cap      int
	nextSeq  uint64           // sequence to be assigned to the next Push (starts at 1)
	headSeq  uint64           // sequence of the oldest live entry
	entries  []ringEntry[T]   // circular buffer; len = cap, index = (seq-headSeq) mod cap
	head     int              // index of headSeq
	size     int              // live entries (≤ cap)
}

type ringEntry[T any] struct {
	seq uint64
	val T
}

func newRing[T any](capacity int) *ring[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &ring[T]{
		cap:     capacity,
		entries: make([]ringEntry[T], capacity),
		nextSeq: 1,
	}
}

// Push assigns a fresh sequence to v and stores it in the ring. Returns
// the assigned sequence. If the ring is full, the oldest entry is dropped
// and headSeq advances.
func (r *ring[T]) Push(v T) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pushLocked(v)
}

// PushWith assigns a fresh sequence, hands it to build to construct the
// value (so the value can embed its own sequence), then stores it.
// Returns the assigned sequence.
func (r *ring[T]) PushWith(build func(seq uint64) T) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	seq := r.nextSeq
	v := build(seq)
	return r.pushLocked(v)
}

func (r *ring[T]) pushLocked(v T) uint64 {
	seq := r.nextSeq
	r.nextSeq++
	if r.size < r.cap {
		idx := (r.head + r.size) % r.cap
		r.entries[idx] = ringEntry[T]{seq: seq, val: v}
		r.size++
		if r.size == 1 {
			r.headSeq = seq
		}
	} else {
		r.entries[r.head] = ringEntry[T]{seq: seq, val: v}
		r.head = (r.head + 1) % r.cap
		r.headSeq++
	}
	return seq
}

// Since returns all entries with sequence > token, in order, plus the
// number of dropped entries (entries that should have followed token but
// were evicted). Token 0 returns all live entries with dropped = headSeq
// (i.e. number of entries evicted since startup).
func (r *ring[T]) Since(token uint64) ([]T, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size == 0 {
		return nil, 0
	}
	startSeq := token + 1
	if startSeq < r.headSeq {
		// Caller is behind; report drops between (token, headSeq).
		dropped := int(r.headSeq - startSeq)
		startSeq = r.headSeq
		out := r.copyFrom(startSeq)
		return out, dropped
	}
	if startSeq >= r.nextSeq {
		return nil, 0
	}
	return r.copyFrom(startSeq), 0
}

func (r *ring[T]) copyFrom(startSeq uint64) []T {
	if startSeq < r.headSeq || startSeq >= r.nextSeq {
		return nil
	}
	skip := int(startSeq - r.headSeq)
	count := r.size - skip
	if count <= 0 {
		return nil
	}
	out := make([]T, 0, count)
	for i := 0; i < count; i++ {
		idx := (r.head + skip + i) % r.cap
		out = append(out, r.entries[idx].val)
	}
	return out
}

// Len returns the number of live entries.
func (r *ring[T]) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

// Last returns the most recently pushed entry, or (zero, false) if empty.
func (r *ring[T]) Last() (T, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var zero T
	if r.size == 0 {
		return zero, false
	}
	idx := (r.head + r.size - 1) % r.cap
	return r.entries[idx].val, true
}

// lruCache is a tiny size-bounded LRU keyed by string with byte-slice
// values. evictionFn is called for each evicted entry (with its size, so
// callers can decrement byte accounting). Safe for concurrent use.
type lruCache struct {
	mu      sync.Mutex
	max     int                  // max entries
	maxBytes int                 // max total bytes; 0 disables byte cap
	bytes   int                  // current total bytes
	order   *list.List           // most recent at front
	entries map[string]*list.Element
}

type lruEntry struct {
	key string
	val []byte
}

func newLRUCache(maxEntries, maxBytes int) *lruCache {
	return &lruCache{
		max:      maxEntries,
		maxBytes: maxBytes,
		order:    list.New(),
		entries:  make(map[string]*list.Element),
	}
}

// Put stores v under key, evicting older entries as needed.
func (c *lruCache) Put(key string, v []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		old := el.Value.(*lruEntry)
		c.bytes -= len(old.val)
		old.val = v
		c.bytes += len(v)
		c.order.MoveToFront(el)
	} else {
		el := c.order.PushFront(&lruEntry{key: key, val: v})
		c.entries[key] = el
		c.bytes += len(v)
	}
	for c.shouldEvict() {
		c.evictOldest()
	}
}

// Get returns the value for key (and refreshes its position), or (nil, false).
func (c *lruCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*lruEntry).val, true
}

func (c *lruCache) shouldEvict() bool {
	if len(c.entries) > c.max {
		return true
	}
	if c.maxBytes > 0 && c.bytes > c.maxBytes {
		return true
	}
	return false
}

func (c *lruCache) evictOldest() {
	el := c.order.Back()
	if el == nil {
		return
	}
	c.order.Remove(el)
	entry := el.Value.(*lruEntry)
	delete(c.entries, entry.key)
	c.bytes -= len(entry.val)
}
