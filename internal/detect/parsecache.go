package detect

import "sync"

const (
	// parseCacheGen is the number of User-Agents one generation holds.
	// The cache keeps at most two generations, so twice this many entries.
	parseCacheGen = 2048
	// parseCacheMaxKey is the longest User-Agent cached, in bytes. Longer ones
	// are parsed every time so a flood of huge headers cannot pin memory.
	parseCacheMaxKey = 1024
)

// parseCache remembers Parse results by User-Agent in two generations.
// Lookups hit cur first, then prev, moving a prev hit into cur. When cur is
// full it becomes prev and the old prev is dropped, which keeps recently used
// keys and bounds the size at two generations. Safe for concurrent use.
type parseCache struct {
	mu   sync.Mutex
	cur  map[string]Info
	prev map[string]Info
}

// parsed is the process-wide cache used by Parse.
var parsed = &parseCache{}

// get returns the cached Info for key. ok is false when key is not cached.
func (c *parseCache) get(key string) (Info, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if info, ok := c.cur[key]; ok {
		return info, true
	}
	info, ok := c.prev[key]
	if ok {
		c.insert(key, info)
	}
	return info, ok
}

// put stores info for key unless key is longer than parseCacheMaxKey.
func (c *parseCache) put(key string, info Info) {
	if len(key) > parseCacheMaxKey {
		return
	}
	c.mu.Lock()
	c.insert(key, info)
	c.mu.Unlock()
}

// insert adds key to cur, rotating generations when cur is full.
// The caller holds c.mu.
func (c *parseCache) insert(key string, info Info) {
	if c.cur == nil || len(c.cur) >= parseCacheGen {
		c.prev = c.cur
		c.cur = make(map[string]Info, parseCacheGen)
	}
	c.cur[key] = info
}
