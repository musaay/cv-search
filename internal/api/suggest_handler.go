package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"cv-search/internal/storage"
)

const popularQueriesTTL = 5 * time.Minute

// SuggestHandler returns autocomplete suggestions for the search box.
//
//	GET /api/search/suggest?q=<prefix>&limit=8
//
// Searches skill names, company names, and person current_positions in the knowledge
// graph. Returns at most `limit` results (default 8, max 20).
// No LLM involved — pure DB prefix search, target p99 < 20ms.
func (a *API) SuggestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	prefix := r.URL.Query().Get("q")
	if len(prefix) < 1 {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}
	if len(prefix) > 100 {
		http.Error(w, "q too long (max 100 chars)", http.StatusBadRequest)
		return
	}

	limit := 8
	if v := r.URL.Query().Get("limit"); v != "" {
		n := 0
		fmt.Sscanf(v, "%d", &n)
		if n > 0 && n <= 20 {
			limit = n
		}
	}

	results, err := a.db.SuggestFromGraph(r.Context(), prefix, limit)
	if err != nil {
		log.Printf("[Suggest] DB error: %v", err)
		http.Error(w, "suggest failed", http.StatusInternalServerError)
		return
	}

	if results == nil {
		results = []storage.SuggestionResult{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// PopularQueriesHandler returns a list of ready-made search queries derived from
// the most common skills and seniority levels in the knowledge graph.
//
//	GET /api/search/popular-queries
//
// Results are cached for 5 minutes — cheap to call on page load.
func (a *API) PopularQueriesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	a.popularQueriesMu.Lock()
	if time.Now().Before(a.popularQueriesExp) && len(a.popularQueriesCache) > 0 {
		cached := a.popularQueriesCache
		a.popularQueriesMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cached)
		return
	}
	a.popularQueriesMu.Unlock()

	// Fetch top skills + seniority levels from DB
	skills, seniorities, err := a.db.GetTopSkillsForQueries(r.Context(), 6, 3)
	if err != nil {
		log.Printf("[PopularQueries] DB error: %v", err)
		http.Error(w, "popular queries failed", http.StatusInternalServerError)
		return
	}

	queries := buildPopularQueries(skills, seniorities)

	a.popularQueriesMu.Lock()
	a.popularQueriesCache = queries
	a.popularQueriesExp = time.Now().Add(popularQueriesTTL)
	a.popularQueriesMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(queries)
}

// buildPopularQueries combines top skills and seniority levels into
// human-readable query strings a recruiter would naturally type.
//
// Strategy:
//   - If there are seniority levels, create "Senior {Skill} Developer" style entries
//   - Always include plain "{Skill} Developer" fallbacks
//   - Cap total at 8 entries
func buildPopularQueries(skills, seniorities []string) []string {
	const maxQueries = 8

	queries := make([]string, 0, maxQueries)
	seen := make(map[string]struct{})

	add := func(q string) {
		if q == "" {
			return
		}
		if _, dup := seen[q]; dup {
			return
		}
		seen[q] = struct{}{}
		queries = append(queries, q)
	}

	// First pass: seniority + skill combinations (most natural for recruiting)
	for _, seniority := range seniorities {
		for _, skill := range skills {
			if len(queries) >= maxQueries {
				break
			}
			add(fmt.Sprintf("%s %s Developer", seniority, skill))
		}
		if len(queries) >= maxQueries {
			break
		}
	}

	// Second pass: plain "{skill} Developer" ones not yet covered
	for _, skill := range skills {
		if len(queries) >= maxQueries {
			break
		}
		add(fmt.Sprintf("%s Developer", skill))
	}

	if len(queries) == 0 {
		// Graceful fallback when DB is empty
		return []string{"Software Developer", "Senior Backend Developer", "Frontend Developer"}
	}
	return queries
}
