package tsdb

import (
	"sync"
)

// regexMatchCache caches which label values match a given regex pattern.
// Since the label values slice (lvs[name]) in MemPostings is append-only,
// cached results remain valid — only newly appended values need evaluation.
// Uses LRU eviction to bound memory for workloads with many unique patterns.
type regexMatchCache struct {
	mu         sync.RWMutex
	entries    map[string]*regexMatchEntry
	lru        []string // keys in LRU order (most recent at end)
	maxEntries int
}

type regexMatchEntry struct {
	mu             sync.Mutex
	matchingValues []string
	checkedCount   int
}

func newRegexMatchCache() *regexMatchCache {
	return &regexMatchCache{
		entries:    make(map[string]*regexMatchEntry),
		maxEntries: 1024,
	}
}

// GetMatchingValues returns label values that match the given function.
// On first call, evaluates all values. On subsequent calls, only evaluates
// values appended since the last call (leveraging append-only property).
func (c *regexMatchCache) GetMatchingValues(allValues []string, name, pattern string, matchFn func(string) bool) []string {
	key := name + "\x00" + pattern

	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok {
		c.mu.Lock()
		entry, ok = c.entries[key]
		if !ok {
			// Evict if at capacity
			if len(c.entries) >= c.maxEntries {
				c.evictOldest()
			}
			entry = &regexMatchEntry{}
			c.entries[key] = entry
		}
		c.touchLocked(key)
		c.mu.Unlock()
	} else {
		c.mu.Lock()
		c.touchLocked(key)
		c.mu.Unlock()
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	if entry.checkedCount == len(allValues) {
		return entry.matchingValues
	}

	for i := entry.checkedCount; i < len(allValues); i++ {
		if matchFn(allValues[i]) {
			entry.matchingValues = append(entry.matchingValues, allValues[i])
		}
	}
	entry.checkedCount = len(allValues)

	return entry.matchingValues
}

func (c *regexMatchCache) touchLocked(key string) {
	// Move to end (most recent)
	for i, k := range c.lru {
		if k == key {
			c.lru = append(c.lru[:i], c.lru[i+1:]...)
			break
		}
	}
	c.lru = append(c.lru, key)
}

func (c *regexMatchCache) evictOldest() {
	if len(c.lru) == 0 {
		return
	}
	oldest := c.lru[0]
	c.lru = c.lru[1:]
	delete(c.entries, oldest)
}

// Clear resets the cache (e.g., on head truncation).
func (c *regexMatchCache) Clear() {
	c.mu.Lock()
	c.entries = make(map[string]*regexMatchEntry)
	c.lru = c.lru[:0]
	c.mu.Unlock()
}
