package graphrag

import (
	"crypto/md5"
	"fmt"
	"sort"
	"sync"
	"time"
)

// LLMCache provides simple in-memory caching for LLM results
type LLMCache struct {
	mu      sync.RWMutex
	entries map[string]*CacheEntry
	ttl     time.Duration
}

type CacheEntry struct {
	Scores    []CandidateScore
	Timestamp time.Time
}

// NewLLMCache creates a new cache with specified TTL
func NewLLMCache(ttl time.Duration) *LLMCache {
	return &LLMCache{
		entries: make(map[string]*CacheEntry),
		ttl:     ttl,
	}
}

// Get retrieves cached scores if available and not expired
func (c *LLMCache) Get(query string, candidateIDs []string) ([]CandidateScore, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := c.generateKey(query, candidateIDs)
	entry, exists := c.entries[key]

	if !exists {
		return nil, false
	}

	// Check if expired
	if time.Since(entry.Timestamp) > c.ttl {
		return nil, false
	}

	return entry.Scores, true
}

// Set stores scores in cache
func (c *LLMCache) Set(query string, candidateIDs []string, scores []CandidateScore) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := c.generateKey(query, candidateIDs)
	c.entries[key] = &CacheEntry{
		Scores:    scores,
		Timestamp: time.Now(),
	}
}

// Clear removes all cache entries
func (c *LLMCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*CacheEntry)
}

// CleanExpired removes expired entries (call periodically)
func (c *LLMCache) CleanExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, entry := range c.entries {
		if now.Sub(entry.Timestamp) > c.ttl {
			delete(c.entries, key)
		}
	}
}

// generateKey creates a unique cache key from query and candidate list
func (c *LLMCache) generateKey(query string, candidateIDs []string) string {
	// Sort candidate IDs for consistent key (order doesn't matter)
	sorted := make([]string, len(candidateIDs))
	copy(sorted, candidateIDs)
	sort.Strings(sorted)
	
	// Combine query + sorted candidate IDs
	data := query
	for _, id := range sorted {
		data += "|" + id
	}
	hash := md5.Sum([]byte(data))
	return fmt.Sprintf("%x", hash)
}
