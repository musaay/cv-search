package graphrag

import (
	"crypto/md5"
	"fmt"
	"math"
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

// NewLLMCache creates a new cache with specified TTL.
// Starts a background goroutine to evict expired entries every 10 minutes.
func NewLLMCache(ttl time.Duration) *LLMCache {
	c := &LLMCache{
		entries: make(map[string]*CacheEntry),
		ttl:     ttl,
	}
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		for range ticker.C {
			c.CleanExpired()
		}
	}()
	return c
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

// ─── Semantic Cache ───────────────────────────────────────────────────────────
// Stores query embeddings and results. On a new query, computes cosine similarity
// against stored embeddings; if similarity >= threshold it is treated as the same
// query and the cached results are returned immediately (bypasses full pipeline).

// SemanticCache caches search results keyed by query embedding similarity.
type SemanticCache struct {
	mu        sync.RWMutex
	entries   []*semanticCacheEntry
	ttl       time.Duration
	threshold float64 // cosine similarity threshold, e.g. 0.95
}

type semanticCacheEntry struct {
	QueryEmbedding []float32
	QueryText      string
	Results        []FusedCandidate
	Timestamp      time.Time
}

const semanticCacheMaxSize = 500 // max in-memory entries; oldest are evicted when exceeded

// NewSemanticCache creates a semantic cache with given TTL and similarity threshold.
func NewSemanticCache(ttl time.Duration, threshold float64) *SemanticCache {
	return &SemanticCache{
		entries:   make([]*semanticCacheEntry, 0, 64),
		ttl:       ttl,
		threshold: threshold,
	}
}

// Get returns cached results if a sufficiently similar query exists and has not expired.
// Returns (results, originalQueryText, found).
func (c *SemanticCache) Get(queryEmbedding []float32) ([]FusedCandidate, string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	now := time.Now()
	bestSim := -1.0
	var bestEntry *semanticCacheEntry

	for _, entry := range c.entries {
		if now.Sub(entry.Timestamp) > c.ttl {
			continue
		}
		sim := cosineSimilarity32(queryEmbedding, entry.QueryEmbedding)
		if sim >= c.threshold && sim > bestSim {
			bestSim = sim
			bestEntry = entry
		}
	}

	if bestEntry != nil {
		return bestEntry.Results, bestEntry.QueryText, true
	}
	return nil, "", false
}

// Set stores results for a query embedding, evicting expired entries first.
// If the cache still exceeds semanticCacheMaxSize after expiry eviction, the oldest
// entries are trimmed to keep memory bounded.
func (c *SemanticCache) Set(queryEmbedding []float32, queryText string, results []FusedCandidate) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict expired entries
	now := time.Now()
	valid := c.entries[:0]
	for _, e := range c.entries {
		if now.Sub(e.Timestamp) <= c.ttl {
			valid = append(valid, e)
		}
	}
	c.entries = valid

	// If still over max capacity, trim oldest entries (entries are appended chronologically)
	if len(c.entries) >= semanticCacheMaxSize {
		trimTo := semanticCacheMaxSize - semanticCacheMaxSize/4 // trim to 75% capacity
		c.entries = c.entries[len(c.entries)-trimTo:]
	}

	c.entries = append(c.entries, &semanticCacheEntry{
		QueryEmbedding: queryEmbedding,
		QueryText:      queryText,
		Results:        results,
		Timestamp:      now,
	})
}

// cosineSimilarity32 computes cosine similarity between two float32 vectors.
func cosineSimilarity32(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
