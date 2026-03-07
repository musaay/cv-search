package graphrag

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"strings"
	"time"
)

// CommunityDetector detects professional communities via k-means on person embeddings.
// Triggered automatically after every CV upload (background_jobs.go triggerCommunityDetection)
// and can also be run manually via cmd/tools/detect_communities.
type CommunityDetector struct {
	db               *sql.DB
	llm              LLMClient
	embeddingService *EmbeddingService

	// K is the number of clusters. 0 = auto-compute: max(6, nPersons/4).
	K int
}

func NewCommunityDetector(db *sql.DB, llm LLMClient, embeddingService *EmbeddingService) *CommunityDetector {
	return &CommunityDetector{
		db:               db,
		llm:              llm,
		embeddingService: embeddingService,
	}
}

// communityPerson holds person node data for clustering.
type communityPerson struct {
	id        int       // graph_nodes.id (integer PK)
	nodeID    string    // graph_nodes.node_id (text)
	embedding []float32 // 1536-dimensional vector
}

// DetectCommunities runs k-means on person embeddings, generates LLM titles/summaries,
// embeds the summaries, and upserts results to graph_communities + community_members.
// Safe to run repeatedly — uses ON CONFLICT DO UPDATE, never hard-deletes.
func (cd *CommunityDetector) DetectCommunities(ctx context.Context, level int) error {
	log.Printf("[CommunityDetect] Starting embedding-based detection (level=%d)", level)

	if cd.embeddingService == nil {
		return fmt.Errorf("embedding service not configured")
	}

	persons, err := cd.loadPersonEmbeddings(ctx)
	if err != nil {
		return fmt.Errorf("failed to load person embeddings: %w", err)
	}
	if len(persons) == 0 {
		log.Printf("[CommunityDetect] No person embeddings found — upload CVs first")
		return nil
	}

	k := cd.K
	if k <= 0 {
		// Auto-compute: grow with data but cap at 30.
		// Beyond 30 clusters the categories become too granular and LLM cost spikes.
		k = cdMin(cdMax(6, len(persons)/4), 30)
	}
	if len(persons) < k {
		k = len(persons)
	}

	log.Printf("[CommunityDetect] %d persons, k=%d", len(persons), k)

	// Purge stale memberships for this level before re-clustering.
	// K may change between runs (new CVs added), so old assignments can be wrong.
	// graph_communities rows are kept and updated in-place (ON CONFLICT DO UPDATE);
	// only the membership table needs a full refresh.
	_, err = cd.db.ExecContext(ctx, `
		DELETE FROM community_members
		WHERE community_id IN (
			SELECT id FROM graph_communities WHERE level = $1
		)
	`, level)
	if err != nil {
		log.Printf("[CommunityDetect] Failed to purge stale memberships (non-fatal): %v", err)
	} else {
		log.Printf("[CommunityDetect] Purged stale community_members for level %d", level)
	}

	assignments, centroids := cdKmeans(persons, k, 50)

	type cluster struct {
		personIntIDs []int
		embeddings   [][]float32
	}
	clusters := make([]cluster, k)
	for i, p := range persons {
		ci := assignments[i]
		clusters[ci].personIntIDs = append(clusters[ci].personIntIDs, p.id)
		clusters[ci].embeddings = append(clusters[ci].embeddings, p.embedding)
	}

	successCount := 0
	for ci, cl := range clusters {
		if len(cl.personIntIDs) == 0 {
			continue
		}
		communityID := fmt.Sprintf("cluster_%d", ci)

		skills, _ := cd.loadTopSkills(ctx, cl.personIntIDs, 15)
		positions, _ := cd.loadCurrentPositions(ctx, cl.personIntIDs, 5)

		title, summary, err := cd.generateCommunityProfile(skills, positions)
		if err != nil {
			log.Printf("[CommunityDetect] cluster %d: LLM failed (%v) — fallback", ci, err)
			n := 3
			if len(skills) < n {
				n = len(skills)
			}
			title = fmt.Sprintf("Cluster %d", ci)
			if n > 0 {
				title = strings.Join(skills[:n], ", ") + " Professionals"
			}
			summary = fmt.Sprintf("A group of %d professionals with shared technical skills.", len(cl.personIntIDs))
		}

		log.Printf("[CommunityDetect] cluster %d (%d members): %q", ci, len(cl.personIntIDs), title)

		var embeddingJSON string
		summaryEmb, embErr := cd.embeddingService.GenerateEmbedding(ctx, title+" "+summary)
		if embErr != nil {
			log.Printf("[CommunityDetect] cluster %d: embed failed (non-fatal): %v", ci, embErr)
		} else {
			b, _ := json.Marshal(summaryEmb)
			embeddingJSON = string(b)
		}

		var gcID int
		var upsertErr error
		if embeddingJSON != "" {
			upsertErr = cd.db.QueryRowContext(ctx, `
				INSERT INTO graph_communities (level, community_id, title, summary, node_count, embedding, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6::vector, NOW())
				ON CONFLICT (level, community_id) DO UPDATE
				  SET title = EXCLUDED.title,
				      summary = EXCLUDED.summary,
				      node_count = EXCLUDED.node_count,
				      embedding = EXCLUDED.embedding,
				      updated_at = NOW()
				RETURNING id
			`, level, communityID, title, summary, len(cl.personIntIDs), embeddingJSON).Scan(&gcID)
		} else {
			upsertErr = cd.db.QueryRowContext(ctx, `
				INSERT INTO graph_communities (level, community_id, title, summary, node_count, updated_at)
				VALUES ($1, $2, $3, $4, $5, NOW())
				ON CONFLICT (level, community_id) DO UPDATE
				  SET title = EXCLUDED.title,
				      summary = EXCLUDED.summary,
				      node_count = EXCLUDED.node_count,
				      updated_at = NOW()
				RETURNING id
			`, level, communityID, title, summary, len(cl.personIntIDs)).Scan(&gcID)
		}
		if upsertErr != nil {
			log.Printf("[CommunityDetect] cluster %d: upsert failed: %v", ci, upsertErr)
			continue
		}

		centroid := centroids[ci]
		for j, nodeIntID := range cl.personIntIDs {
			strength := cdCosineSimilarity(cl.embeddings[j], centroid)
			_, err := cd.db.ExecContext(ctx, `
				INSERT INTO community_members (community_id, node_id, membership_strength)
				VALUES ($1, $2, $3)
				ON CONFLICT (community_id, node_id) DO UPDATE
				  SET membership_strength = EXCLUDED.membership_strength
			`, gcID, nodeIntID, strength)
			if err != nil {
				log.Printf("[CommunityDetect] cluster %d member %d: insert failed: %v", ci, nodeIntID, err)
			}
		}

		successCount++
		if ci < k-1 {
			time.Sleep(300 * time.Millisecond)
		}
	}

	log.Printf("[CommunityDetect] Done: %d/%d communities written", successCount, k)
	return nil
}

func (cd *CommunityDetector) loadPersonEmbeddings(ctx context.Context) ([]communityPerson, error) {
	rows, err := cd.db.QueryContext(ctx, `
		SELECT id, node_id, embedding::text
		FROM graph_nodes
		WHERE node_type = 'person' AND embedding IS NOT NULL
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var persons []communityPerson
	for rows.Next() {
		var p communityPerson
		var embText string
		if err := rows.Scan(&p.id, &p.nodeID, &embText); err != nil {
			continue
		}
		var emb []float32
		if err := json.Unmarshal([]byte(embText), &emb); err != nil {
			log.Printf("[CommunityDetect] Failed to parse embedding for %s: %v", p.nodeID, err)
			continue
		}
		p.embedding = emb
		persons = append(persons, p)
	}
	return persons, rows.Err()
}

func (cd *CommunityDetector) loadTopSkills(ctx context.Context, personIntIDs []int, topN int) ([]string, error) {
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
		SELECT sn.properties->>'name', COUNT(*) AS cnt
		FROM graph_edges e
		JOIN graph_nodes sn ON sn.id = e.target_node_id AND sn.node_type = 'skill'
		WHERE e.source_node_id IN (%s)
		  AND e.edge_type = 'HAS_SKILL'
		  AND sn.properties->>'name' IS NOT NULL
		GROUP BY sn.properties->>'name'
		ORDER BY cnt DESC
		LIMIT $%d
	`, strings.Join(placeholders, ","), len(personIntIDs)+1)

	rows, err := cd.db.QueryContext(ctx, q, args...)
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

func (cd *CommunityDetector) loadCurrentPositions(ctx context.Context, personIntIDs []int, n int) ([]string, error) {
	if len(personIntIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(personIntIDs))
	args := make([]interface{}, len(personIntIDs))
	for i, id := range personIntIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	args = append(args, n)

	q := fmt.Sprintf(`
		SELECT DISTINCT properties->>'current_position'
		FROM graph_nodes
		WHERE id IN (%s)
		  AND node_type = 'person'
		  AND properties->>'current_position' IS NOT NULL
		  AND properties->>'current_position' != ''
		LIMIT $%d
	`, strings.Join(placeholders, ","), len(personIntIDs)+1)

	rows, err := cd.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var positions []string
	for rows.Next() {
		var pos string
		if err := rows.Scan(&pos); err != nil {
			continue
		}
		positions = append(positions, pos)
	}
	return positions, rows.Err()
}

func (cd *CommunityDetector) generateCommunityProfile(skills []string, positions []string) (string, string, error) {
	skillStr := strings.Join(skills, ", ")
	if skillStr == "" {
		skillStr = "(no skills data)"
	}
	posStr := strings.Join(positions, ", ")
	if posStr == "" {
		posStr = "(no position data)"
	}

	prompt := fmt.Sprintf(`You are analyzing a cluster of professional CVs detected by k-means clustering on semantic embeddings.

Top shared skills: %s
Common job titles: %s

Generate a community profile in JSON format:
{
  "title": "Short role label, max 4 words (e.g. 'Backend Java Developers', 'Business Analysts', 'DevOps Engineers')",
  "summary": "2-3 sentences: what kind of professionals they are, domain, key skills. Be specific."
}

Respond with valid JSON only. No markdown.`, skillStr, posStr)

	raw, err := cd.llm.Generate(prompt)
	if err != nil {
		return "", "", err
	}

	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return "", "", fmt.Errorf("no JSON in LLM response")
	}
	raw = raw[start : end+1]

	var result struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return "", "", fmt.Errorf("JSON parse failed: %v", err)
	}
	if result.Title == "" || result.Summary == "" {
		return "", "", fmt.Errorf("empty title or summary from LLM")
	}
	return result.Title, result.Summary, nil
}

// -----------------------------------------------------------------------
// K-means helpers (cosine similarity on unit-normalized OpenAI embeddings)
// -----------------------------------------------------------------------

func cdKmeans(persons []communityPerson, k int, maxIter int) ([]int, [][]float32) {
	n := len(persons)
	if n == 0 || k <= 0 {
		return nil, nil
	}
	dim := len(persons[0].embedding)

	rng := rand.New(rand.NewSource(42)) // fixed seed for reproducibility
	indices := rng.Perm(n)[:k]
	centroids := make([][]float32, k)
	for i, idx := range indices {
		centroids[i] = make([]float32, dim)
		copy(centroids[i], persons[idx].embedding)
	}

	assignments := make([]int, n)

	for iter := 0; iter < maxIter; iter++ {
		changed := false
		for i, p := range persons {
			bestCI, bestSim := 0, math.Inf(-1)
			for ci, c := range centroids {
				if sim := cdDotProduct(p.embedding, c); sim > bestSim {
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
			log.Printf("[CommunityDetect] K-means converged after %d iterations", iter+1)
			break
		}
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
				copy(newCentroids[ci], persons[rng.Intn(n)].embedding)
				continue
			}
			for d := 0; d < dim; d++ {
				newCentroids[ci][d] /= float32(counts[ci])
			}
			if norm := cdL2norm(newCentroids[ci]); norm > 0 {
				for d := 0; d < dim; d++ {
					newCentroids[ci][d] /= norm
				}
			}
			centroids[ci] = newCentroids[ci]
		}
	}

	return assignments, centroids
}

func cdDotProduct(a, b []float32) float64 {
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

func cdL2norm(v []float32) float32 {
	var sum float32
	for _, x := range v {
		sum += x * x
	}
	return float32(math.Sqrt(float64(sum)))
}

func cdCosineSimilarity(a, b []float32) float64 {
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

func cdMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func cdMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

