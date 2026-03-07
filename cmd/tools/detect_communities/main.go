// detect_communities runs k-means clustering on person node embeddings,
// then generates LLM summaries and writes communities to graph_communities + community_members.
//
// Usage:
//
//	go run ./cmd/tools/detect_communities/ [flags]
//
// Flags:
//
//	--k       Number of clusters (default 10)
//	--level   Community level stored in graph_communities.level (default 0)
//	--dry-run Print cluster summaries without writing to DB
//
// Required env vars: DATABASE_URL, OPENAI_API_KEY, LLM_PROVIDER, LLM_MODEL (+ GROQ_API_KEY if provider=groq)
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"strings"
	"time"

	"cv-search/internal/graphrag"
	"cv-search/internal/llm"
	"cv-search/internal/storage"
)

// personNode holds the data loaded from graph_nodes for clustering.
type personNode struct {
	id        int       // graph_nodes.id (integer PK)
	nodeID    string    // graph_nodes.node_id (text)
	name      string    // from properties->>'name'
	position  string    // from properties->>'current_position'
	embedding []float32 // 1536-dimensional vector
}

func main() {
	var k int
	var level int
	var dryRun bool
	flag.IntVar(&k, "k", 10, "Number of clusters for k-means")
	flag.IntVar(&level, "level", 0, "Community level (stored in graph_communities.level)")
	flag.BoolVar(&dryRun, "dry-run", false, "Print cluster info without writing to DB")
	flag.Parse()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	openAIKey := os.Getenv("OPENAI_API_KEY")
	if openAIKey == "" {
		log.Fatal("OPENAI_API_KEY is required for generating community embeddings")
	}
	llmProvider := os.Getenv("LLM_PROVIDER")
	llmModel := os.Getenv("LLM_MODEL")
	var llmAPIKey string
	switch llmProvider {
	case "openai":
		llmAPIKey = openAIKey
	case "groq":
		llmAPIKey = os.Getenv("GROQ_API_KEY")
	default:
		log.Fatalf("LLM_PROVIDER must be 'openai' or 'groq', got: %q", llmProvider)
	}

	db, err := storage.NewDB(dbURL)
	if err != nil {
		log.Fatalf("failed to connect to db: %v", err)
	}
	defer db.Close()

	conn := db.GetConnection()
	llmSvc := llm.NewService(llmProvider, llmAPIKey, llmModel)
	embSvc := graphrag.NewEmbeddingService(openAIKey, conn)
	ctx := context.Background()

	// Step 1: Load all person nodes with embeddings.
	log.Println("Loading person embeddings from graph_nodes...")
	persons, err := loadPersonEmbeddings(ctx, conn)
	if err != nil {
		log.Fatalf("failed to load embeddings: %v", err)
	}
	log.Printf("Loaded %d persons with embeddings", len(persons))

	if len(persons) < k {
		log.Fatalf("Not enough persons with embeddings (%d) for k=%d clusters. Upload more CVs first.", len(persons), k)
	}

	// Step 2: K-means clustering on embeddings.
	log.Printf("Running k-means with k=%d (max 50 iterations)...", k)
	assignments, centroids := kmeans(persons, k, 50)

	// Step 3: Group persons by cluster and collect their positions.
	type clusterData struct {
		personIntIDs []int       // graph_nodes.id values for members
		embeddings   [][]float32 // per-member embedding (for membership_strength calculation)
		positions    []string    // current_position strings (for LLM prompt)
	}
	clusters := make([]clusterData, k)
	for i, p := range persons {
		ci := assignments[i]
		clusters[ci].personIntIDs = append(clusters[ci].personIntIDs, p.id)
		clusters[ci].embeddings = append(clusters[ci].embeddings, p.embedding)
		if p.position != "" {
			clusters[ci].positions = append(clusters[ci].positions, p.position)
		}
	}

	// Step 4: Load top skills per cluster from graph_edges.
	log.Println("Loading top skills per cluster...")
	type clusterInfo struct {
		idx      int
		data     clusterData
		centroid []float32
		skills   []string
	}
	var activeClusters []clusterInfo
	for ci := range clusters {
		if len(clusters[ci].personIntIDs) == 0 {
			log.Printf("[cluster %d] empty — skipping", ci)
			continue
		}
		skills, err := loadTopSkillsForPersons(ctx, conn, clusters[ci].personIntIDs, 15)
		if err != nil {
			log.Printf("[cluster %d] failed to load skills (non-fatal): %v", ci, err)
		}
		activeClusters = append(activeClusters, clusterInfo{
			idx:      ci,
			data:     clusters[ci],
			centroid: centroids[ci],
			skills:   skills,
		})
	}

	// Step 5: Generate LLM title + summary for each cluster.
	log.Println("Generating LLM summaries...")
	type communityResult struct {
		clusterIdx  int
		communityID string
		title       string
		summary     string
		nodeCount   int
		centroid    []float32
		data        clusterData
	}
	var results []communityResult
	for _, cl := range activeClusters {
		communityID := fmt.Sprintf("cluster_%d", cl.idx)
		topPositions := uniqueTop(cl.data.positions, 5)

		title, summary, err := generateCommunitySummary(llmSvc, cl.skills, topPositions)
		if err != nil {
			log.Printf("[cluster %d] LLM failed: %v — using fallback", cl.idx, err)
			topN := min(3, len(cl.skills))
			title = fmt.Sprintf("Cluster %d", cl.idx)
			summary = fmt.Sprintf("A group of %d professionals with skills: %s", len(cl.data.personIntIDs), strings.Join(cl.skills[:topN], ", "))
		}

		log.Printf("[cluster %d] %q — %d members | skills: %v | title: %s",
			cl.idx, communityID, len(cl.data.personIntIDs),
			cl.skills[:min(5, len(cl.skills))], title)

		results = append(results, communityResult{
			clusterIdx:  cl.idx,
			communityID: communityID,
			title:       title,
			summary:     summary,
			nodeCount:   len(cl.data.personIntIDs),
			centroid:    cl.centroid,
			data:        cl.data,
		})
	}

	if dryRun {
		log.Println("\n--- DRY RUN COMPLETE (nothing written to DB) ---")
		for _, r := range results {
			log.Printf("  [cluster_%d] %s (%d members)\n    %s", r.clusterIdx, r.title, r.nodeCount, r.summary)
		}
		return
	}

	// Step 6: Embed summaries and write to graph_communities + community_members.
	log.Println("Writing communities to DB...")
	for i, r := range results {
		// 6a. Embed the title+summary for query-time similarity lookup.
		log.Printf("[cluster %d/%d] Generating embedding for %q...", i+1, len(results), r.title)
		summaryEmb, embErr := embSvc.GenerateEmbedding(ctx, r.title+" "+r.summary)
		if embErr != nil {
			log.Printf("[cluster %d] embedding failed (will store without embedding): %v", r.clusterIdx, embErr)
		}

		// 6b. Upsert graph_communities.
		var gcID int
		var upsertErr error
		if summaryEmb != nil {
			embBytes, _ := json.Marshal(summaryEmb)
			upsertErr = conn.QueryRowContext(ctx, `
				INSERT INTO graph_communities (level, community_id, title, summary, node_count, embedding, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6::vector, NOW())
				ON CONFLICT (level, community_id) DO UPDATE
				  SET title = EXCLUDED.title,
				      summary = EXCLUDED.summary,
				      node_count = EXCLUDED.node_count,
				      embedding = EXCLUDED.embedding,
				      updated_at = NOW()
				RETURNING id
			`, level, r.communityID, r.title, r.summary, r.nodeCount, string(embBytes)).Scan(&gcID)
		} else {
			upsertErr = conn.QueryRowContext(ctx, `
				INSERT INTO graph_communities (level, community_id, title, summary, node_count, updated_at)
				VALUES ($1, $2, $3, $4, $5, NOW())
				ON CONFLICT (level, community_id) DO UPDATE
				  SET title = EXCLUDED.title,
				      summary = EXCLUDED.summary,
				      node_count = EXCLUDED.node_count,
				      updated_at = NOW()
				RETURNING id
			`, level, r.communityID, r.title, r.summary, r.nodeCount).Scan(&gcID)
		}
		if upsertErr != nil {
			log.Printf("[cluster %d] failed to upsert graph_communities: %v", r.clusterIdx, upsertErr)
			continue
		}

		// 6c. Upsert community_members with membership_strength = cosine similarity to centroid.
		membersFailed := 0
		for j, nodeIntID := range r.data.personIntIDs {
			strength := cosineSimilarity(r.data.embeddings[j], r.centroid)
			_, err := conn.ExecContext(ctx, `
				INSERT INTO community_members (community_id, node_id, membership_strength)
				VALUES ($1, $2, $3)
				ON CONFLICT (community_id, node_id) DO UPDATE
				  SET membership_strength = EXCLUDED.membership_strength
			`, gcID, nodeIntID, strength)
			if err != nil {
				membersFailed++
			}
		}

		log.Printf("[cluster %d] %q — wrote %d members (%d failed)",
			r.clusterIdx, r.title, len(r.data.personIntIDs)-membersFailed, membersFailed)

		// Brief pause to stay within embedding API rate limits.
		if i < len(results)-1 {
			time.Sleep(300 * time.Millisecond)
		}
	}

	log.Printf("Done. %d communities written to DB.", len(results))
}

// loadPersonEmbeddings fetches all person nodes that have embeddings.
func loadPersonEmbeddings(ctx context.Context, db *sql.DB) ([]personNode, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id,
		       node_id,
		       COALESCE(properties->>'name', ''),
		       COALESCE(properties->>'current_position', ''),
		       embedding::text
		FROM graph_nodes
		WHERE node_type = 'person'
		  AND embedding IS NOT NULL
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var persons []personNode
	for rows.Next() {
		var p personNode
		var embText string
		if err := rows.Scan(&p.id, &p.nodeID, &p.name, &p.position, &embText); err != nil {
			log.Printf("scan error: %v", err)
			continue
		}
		emb, err := parseVectorString(embText)
		if err != nil {
			log.Printf("failed to parse embedding for node %s: %v", p.nodeID, err)
			continue
		}
		p.embedding = emb
		persons = append(persons, p)
	}
	return persons, rows.Err()
}

// loadTopSkillsForPersons returns the top N most common skills across a set of person nodes.
func loadTopSkillsForPersons(ctx context.Context, db *sql.DB, personIntIDs []int, topN int) ([]string, error) {
	if len(personIntIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(personIntIDs))
	args := make([]interface{}, len(personIntIDs))
	for i, id := range personIntIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	args = append(args, topN)

	q := fmt.Sprintf(`
		SELECT sn.properties->>'name' AS skill_name, COUNT(*) AS cnt
		FROM graph_edges e
		JOIN graph_nodes sn ON sn.id = e.target_node_id AND sn.node_type = 'skill'
		WHERE e.source_node_id IN (%s)
		  AND e.edge_type = 'HAS_SKILL'
		  AND sn.properties->>'name' IS NOT NULL
		GROUP BY skill_name
		ORDER BY cnt DESC
		LIMIT $%d
	`, strings.Join(placeholders, ","), len(personIntIDs)+1)

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var skills []string
	for rows.Next() {
		var name string
		var cnt int
		if err := rows.Scan(&name, &cnt); err != nil {
			continue
		}
		if name != "" {
			skills = append(skills, name)
		}
	}
	return skills, rows.Err()
}

// generateCommunitySummary calls the LLM to produce a short title and summary for a cluster.
func generateCommunitySummary(svc *llm.Service, skills []string, positions []string) (string, string, error) {
	skillStr := strings.Join(skills, ", ")
	if skillStr == "" {
		skillStr = "(no skills data)"
	}
	posStr := strings.Join(positions, ", ")
	if posStr == "" {
		posStr = "(no position data)"
	}

	prompt := fmt.Sprintf(`You are analyzing a cluster of professional CVs detected automatically by k-means clustering.

Top skills shared by this group: %s
Common job titles in this group: %s

Generate a community profile in the following JSON format:
{
  "title": "Short role label, max 4 words (examples: 'Backend Java Developers', 'Business Analysts', 'DevOps Engineers', 'QA Test Engineers')",
  "summary": "2-3 sentences describing this professional cluster: what kind of professionals they are, what domain they work in, key technical skills. Be specific."
}

Respond with valid JSON only. No markdown, no explanation.`, skillStr, posStr)

	raw, err := svc.Generate(prompt)
	if err != nil {
		return "", "", err
	}

	// Extract and parse the JSON object from the response.
	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return "", "", fmt.Errorf("no JSON object in LLM response: %q", truncate(raw, 200))
	}
	raw = raw[start : end+1]

	var result struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return "", "", fmt.Errorf("failed to parse LLM JSON: %v — raw: %q", err, truncate(raw, 200))
	}
	if result.Title == "" || result.Summary == "" {
		return "", "", fmt.Errorf("LLM returned empty title or summary: %q", raw)
	}
	return result.Title, result.Summary, nil
}

// kmeans clusters persons into k groups using cosine similarity.
// OpenAI text-embedding-3-small embeddings are unit-normalized, so dot product == cosine similarity.
// Returns: assignments[i] = cluster index for persons[i], centroids[ci] = unit-normalized centroid.
func kmeans(persons []personNode, k int, maxIter int) ([]int, [][]float32) {
	n := len(persons)
	dim := len(persons[0].embedding)

	// Seed with a fixed value for reproducible results.
	rng := rand.New(rand.NewSource(42))

	// Initialize centroids by picking k random distinct person embeddings.
	indices := rng.Perm(n)[:k]
	centroids := make([][]float32, k)
	for i, idx := range indices {
		centroids[i] = make([]float32, dim)
		copy(centroids[i], persons[idx].embedding)
	}

	assignments := make([]int, n)

	for iter := 0; iter < maxIter; iter++ {
		changed := false

		// Assignment step: assign each person to the nearest centroid.
		for i, p := range persons {
			bestCI := 0
			bestSim := math.Inf(-1)
			for ci, c := range centroids {
				sim := dotProduct(p.embedding, c)
				if sim > bestSim {
					bestSim = sim
					bestCI = ci
				}
			}
			if assignments[i] != bestCI {
				assignments[i] = bestCI
				changed = true
			}
		}

		if !changed {
			log.Printf("K-means converged after %d iterations", iter+1)
			break
		}

		// Update step: recompute centroids as mean of assigned embeddings, then normalize.
		newCentroids := make([][]float32, k)
		counts := make([]int, k)
		for ci := range newCentroids {
			newCentroids[ci] = make([]float32, dim)
		}
		for i, p := range persons {
			ci := assignments[i]
			counts[ci]++
			for d := 0; d < dim; d++ {
				newCentroids[ci][d] += p.embedding[d]
			}
		}
		for ci := range newCentroids {
			if counts[ci] == 0 {
				// Empty cluster: reinitialize with a random person.
				copy(newCentroids[ci], persons[rng.Intn(n)].embedding)
				continue
			}
			// Mean
			for d := 0; d < dim; d++ {
				newCentroids[ci][d] /= float32(counts[ci])
			}
			// Normalize to unit length (required for dot product == cosine similarity).
			norm := l2norm(newCentroids[ci])
			if norm > 0 {
				for d := 0; d < dim; d++ {
					newCentroids[ci][d] /= norm
				}
			}
			centroids[ci] = newCentroids[ci]
		}
	}

	return assignments, centroids
}

// parseVectorString parses a pgvector text representation "[0.1,0.2,...]" into []float32.
// pgvector returns vectors in the same bracket format as a JSON array, so json.Unmarshal works directly.
func parseVectorString(s string) ([]float32, error) {
	var floats []float32
	if err := json.Unmarshal([]byte(s), &floats); err != nil {
		return nil, fmt.Errorf("vector parse error: %w (input prefix: %q)", err, truncate(s, 50))
	}
	return floats, nil
}

// cosineSimilarity computes cosine similarity between two vectors.
func cosineSimilarity(a, b []float32) float64 {
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// dotProduct computes the dot product of two float32 slices (fast path for unit vectors).
func dotProduct(a, b []float32) float64 {
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

// l2norm computes the L2 norm of a float32 slice.
func l2norm(v []float32) float32 {
	var sum float32
	for _, x := range v {
		sum += x * x
	}
	return float32(math.Sqrt(float64(sum)))
}

// uniqueTop returns the first n unique non-empty strings from items.
func uniqueTop(items []string, n int) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range items {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			result = append(result, s)
			if len(result) >= n {
				break
			}
		}
	}
	return result
}

// truncate returns at most n bytes of s for safe error messages.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
