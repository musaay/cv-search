package graphrag

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
)

// EnhancedSearchEngine combines Vector + Community + LLM search (Microsoft GraphRAG style)
type EnhancedSearchEngine struct {
	db                *sql.DB
	llm               LLMClient
	embeddingService  *EmbeddingService
	communityDetector *CommunityDetector
}

func NewEnhancedSearchEngine(db *sql.DB, llm LLMClient, embeddingAPIKey string) *EnhancedSearchEngine {
	return &EnhancedSearchEngine{
		db:                db,
		llm:               llm,
		embeddingService:  NewEmbeddingService(embeddingAPIKey, db),
		communityDetector: NewCommunityDetector(db, llm),
	}
}

// GetEmbeddingService returns the embedding service
func (s *EnhancedSearchEngine) GetEmbeddingService() *EmbeddingService {
	return s.embeddingService
}

// GetCommunityDetector returns the community detector
func (s *EnhancedSearchEngine) GetCommunityDetector() *CommunityDetector {
	return s.communityDetector
}

// EnhancedSearchResult includes vector similarity and community insights
type EnhancedSearchResult struct {
	Query               string               `json:"query"`
	Candidates          []LLMRankedCandidate `json:"candidates"`
	Summary             string               `json:"summary"`
	TotalFound          int                  `json:"total_found"`
	Reasoning           string               `json:"reasoning"`
	RelevantCommunities []CommunityInsight   `json:"relevant_communities,omitempty"`
	SearchMethod        string               `json:"search_method"` // "vector+llm" or "llm-only"
}

// CommunityInsight represents a relevant community for the query
type CommunityInsight struct {
	CommunityID string  `json:"community_id"`
	Title       string  `json:"title"`
	Summary     string  `json:"summary"`
	MemberCount int     `json:"member_count"`
	Relevance   float64 `json:"relevance"`
}

// Search performs enhanced semantic search with vector similarity and community detection
func (s *EnhancedSearchEngine) Search(ctx context.Context, query string) (*EnhancedSearchResult, error) {
	log.Printf("[Enhanced Search] Starting Microsoft GraphRAG-style search for: %s", query)

	// Step 1: Vector similarity search to narrow candidates
	candidateIDs, err := s.vectorSearch(ctx, query, 50) // Get top 50 by vector similarity
	if err != nil || len(candidateIDs) == 0 {
		log.Printf("[Enhanced Search] Vector search failed or empty, falling back to all candidates")
		// Fallback to LLM-only search
		return s.llmOnlySearch(ctx, query)
	}

	log.Printf("[Enhanced Search] Vector search found %d similar candidates", len(candidateIDs))

	// Step 2: Load full candidate profiles
	candidates, err := s.loadCandidatesByIDs(ctx, candidateIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to load candidates: %w", err)
	}

	// Step 3: Find relevant communities
	communities, err := s.findRelevantCommunities(ctx, query)
	if err != nil {
		log.Printf("[Enhanced Search] Community search failed: %v", err)
		communities = []CommunityInsight{}
	}

	log.Printf("[Enhanced Search] Found %d relevant communities", len(communities))

	// Step 4: LLM ranking with community context
	rankedCandidates, reasoning, err := s.llmRankWithCommunities(ctx, query, candidates, communities)
	if err != nil {
		return nil, fmt.Errorf("LLM ranking failed: %w", err)
	}

	// Step 5: Generate enhanced summary
	summary := s.generateEnhancedSummary(query, rankedCandidates, communities, reasoning)

	return &EnhancedSearchResult{
		Query:               query,
		Candidates:          rankedCandidates,
		Summary:             summary,
		TotalFound:          len(rankedCandidates),
		Reasoning:           reasoning,
		RelevantCommunities: communities,
		SearchMethod:        "vector+community+llm",
	}, nil
}

// vectorSearch uses embedding similarity to find relevant candidates
func (s *EnhancedSearchEngine) vectorSearch(ctx context.Context, query string, topK int) ([]string, error) {
	nodeIDs, similarities, err := s.embeddingService.SimilaritySearch(ctx, query, topK)
	if err != nil {
		return nil, err
	}

	log.Printf("[Enhanced Search] Vector search returned %d results (top similarity: %.3f)",
		len(nodeIDs), similarities[0])

	// Filter to only person nodes
	var personIDs []string
	for _, nodeID := range nodeIDs {
		if strings.HasPrefix(nodeID, "person_") {
			personIDs = append(personIDs, nodeID)
		}
	}

	return personIDs, nil
}

// loadCandidatesByIDs loads specific candidates by their node IDs
func (s *EnhancedSearchEngine) loadCandidatesByIDs(ctx context.Context, nodeIDs []string) ([]CandidateResult, error) {
	if len(nodeIDs) == 0 {
		return []CandidateResult{}, nil
	}

	// Build IN clause
	placeholders := make([]string, len(nodeIDs))
	args := make([]interface{}, len(nodeIDs))
	for i, id := range nodeIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	query := fmt.Sprintf(`
		SELECT node_id, properties
		FROM graph_nodes
		WHERE node_type = 'person'
		  AND node_id IN (%s)
	`, strings.Join(placeholders, ","))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []CandidateResult
	for rows.Next() {
		var result CandidateResult
		var propsJSON []byte

		if err := rows.Scan(&result.PersonID, &propsJSON); err != nil {
			continue
		}

		// Parse properties
		var props map[string]interface{}
		if err := json.Unmarshal(propsJSON, &props); err != nil {
			continue
		}

		// Extract basic info
		if cvID, ok := props["cv_id"].(float64); ok {
			result.CVID = int(cvID)
		}
		if name, ok := props["name"].(string); ok {
			result.Name = name
		}
		if pos, ok := props["current_position"].(string); ok {
			result.CurrentPosition = pos
		}
		if sen, ok := props["seniority"].(string); ok {
			result.Seniority = sen
		}
		result.TotalExperience = props["total_experience_years"]

		// Enrich with skills, companies, education
		s.enrichCandidate(ctx, &result)

		candidates = append(candidates, result)
	}

	return candidates, nil
}

// findRelevantCommunities uses vector similarity to find relevant communities
func (s *EnhancedSearchEngine) findRelevantCommunities(ctx context.Context, query string) ([]CommunityInsight, error) {
	// Generate query embedding
	queryEmbedding, err := s.embeddingService.GenerateEmbedding(ctx, query)
	if err != nil {
		return nil, err
	}

	embeddingJSON, _ := json.Marshal(queryEmbedding)

	// Find similar communities
	rows, err := s.db.QueryContext(ctx, `
		SELECT 
			community_id,
			title,
			summary,
			node_count,
			1 - (embedding <=> $1::vector) as relevance
		FROM graph_communities
		WHERE embedding IS NOT NULL
		  AND level = 0
		ORDER BY embedding <=> $1::vector
		LIMIT 5
	`, string(embeddingJSON))

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var communities []CommunityInsight
	for rows.Next() {
		var c CommunityInsight
		if err := rows.Scan(&c.CommunityID, &c.Title, &c.Summary, &c.MemberCount, &c.Relevance); err != nil {
			continue
		}
		communities = append(communities, c)
	}

	return communities, nil
}

// llmRankWithCommunities ranks candidates with community context (Microsoft GraphRAG approach)
func (s *EnhancedSearchEngine) llmRankWithCommunities(
	ctx context.Context,
	query string,
	candidates []CandidateResult,
	communities []CommunityInsight,
) ([]LLMRankedCandidate, string, error) {

	// Build candidate profiles
	candidateProfiles := buildCandidateProfiles(candidates)

	// Build community context
	communityContext := ""
	if len(communities) > 0 {
		communityContext = "\n\nRELEVANT KNOWLEDGE GRAPH COMMUNITIES:\n\n"
		for i, comm := range communities {
			communityContext += fmt.Sprintf("%d. %s (%.0f%% relevant)\n",
				i+1, comm.Title, comm.Relevance*100)
			communityContext += fmt.Sprintf("   %s\n", comm.Summary)
			communityContext += fmt.Sprintf("   Members: %d entities\n\n", comm.MemberCount)
		}
	}

	prompt := fmt.Sprintf(`You are an expert technical recruiter analyzing candidates using a knowledge graph.

USER QUERY: "%s"
%s
CANDIDATE DATABASE (%d candidates pre-filtered by vector similarity):

%s

TASK:
Analyze each candidate considering:
1. Vector similarity already filtered these as potentially relevant
2. Community insights show related skill/company patterns
3. Semantic matching (e.g., "K8s" = "Kubernetes", "Bank" ~ "ING Bank")
4. Domain expertise and career progression
5. Contextual relevance based on query emphasis

OUTPUT JSON (return ONLY valid JSON):
{
  "top_matches": [
    {
      "name": "Candidate Name",
      "fit": "excellent|good|fair|poor",
      "reasoning": "Why this candidate matches (2-3 sentences with specific evidence)",
      "key_strengths": ["Strength 1", "Strength 2", "Strength 3"]
    }
  ],
  "overall_reasoning": "1-2 sentences explaining matching logic and patterns"
}

Include only "excellent" or "good" fits.
`, query, communityContext, len(candidates), candidateProfiles)

	response, err := s.llm.Generate(prompt)
	if err != nil {
		return nil, "", err
	}

	// Parse LLM response
	response = strings.TrimSpace(response)
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	var llmResult struct {
		TopMatches []struct {
			Name         string   `json:"name"`
			Fit          string   `json:"fit"`
			Reasoning    string   `json:"reasoning"`
			KeyStrengths []string `json:"key_strengths"`
		} `json:"top_matches"`
		OverallReasoning string `json:"overall_reasoning"`
	}

	if err := json.Unmarshal([]byte(response), &llmResult); err != nil {
		log.Printf("[Enhanced Search] Failed to parse LLM response: %v", err)
		return nil, "", fmt.Errorf("failed to parse LLM response: %w", err)
	}

	// NO MORE LOCAL SCORING! Pure LLM approach
	var rankedCandidates []LLMRankedCandidate
	for _, match := range llmResult.TopMatches {
		for _, candidate := range candidates {
			if candidate.Name == match.Name {
				// Pure LLM score (no local heuristics)
				var llmScore float64
				switch strings.ToLower(match.Fit) {
				case "excellent":
					llmScore = 95.0
				case "good":
					llmScore = 80.0
				case "fair":
					llmScore = 60.0
				case "poor":
					llmScore = 30.0
				default:
					llmScore = 50.0
				}

				rankedCandidates = append(rankedCandidates, LLMRankedCandidate{
					CVID:            candidate.CVID,
					PersonID:        candidate.PersonID,
					Name:            candidate.Name,
					CurrentPosition: candidate.CurrentPosition,
					Seniority:       candidate.Seniority,
					TotalExperience: candidate.TotalExperience,
					Skills:          candidate.Skills,
					Companies:       candidate.Companies,
					Education:       candidate.Education,
					Fit:             match.Fit,
					Reasoning:       match.Reasoning,
					KeyStrengths:    match.KeyStrengths,
					MatchScore:      llmScore,
					FinalScore:      llmScore, // Pure LLM score
				})
				break
			}
		}
	}

	// Sort by FinalScore descending (pure LLM ranking)
	sort.Slice(rankedCandidates, func(i, j int) bool {
		return rankedCandidates[i].FinalScore > rankedCandidates[j].FinalScore
	})

	return rankedCandidates, llmResult.OverallReasoning, nil
}

// llmOnlySearch fallback when vector search is unavailable
func (s *EnhancedSearchEngine) llmOnlySearch(ctx context.Context, query string) (*EnhancedSearchResult, error) {
	// Use basic LLM search
	basicEngine := NewLLMSearchEngine(s.db, s.llm)
	result, err := basicEngine.Search(ctx, query)
	if err != nil {
		return nil, err
	}

	return &EnhancedSearchResult{
		Query:        result.Query,
		Candidates:   result.Candidates,
		Summary:      result.Summary,
		TotalFound:   result.TotalFound,
		Reasoning:    result.Reasoning,
		SearchMethod: "llm-only (vector unavailable)",
	}, nil
}

// enrichCandidate fetches skills, companies, education
func (s *EnhancedSearchEngine) enrichCandidate(ctx context.Context, candidate *CandidateResult) {
	// Get internal ID
	var internalID int
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM graph_nodes WHERE node_id = $1`,
		candidate.PersonID,
	).Scan(&internalID)

	if err != nil {
		return
	}

	// Fetch skills
	rows, _ := s.db.QueryContext(ctx, `
		SELECT s.properties->>'name', COALESCE(s.properties->>'proficiency', '')
		FROM graph_edges e
		JOIN graph_nodes s ON e.target_node_id = s.id
		WHERE e.source_node_id = $1 AND e.edge_type = 'HAS_SKILL'
	`, internalID)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var skill SkillNode
			rows.Scan(&skill.Name, &skill.Proficiency)
			candidate.Skills = append(candidate.Skills, skill)
		}
	}

	// Fetch companies
	rows, _ = s.db.QueryContext(ctx, `
		SELECT c.properties->>'name', COALESCE(c.properties->>'position', ''), 
		       COALESCE((c.properties->>'is_current')::boolean, false)
		FROM graph_edges e
		JOIN graph_nodes c ON e.target_node_id = c.id
		WHERE e.source_node_id = $1 AND e.edge_type = 'WORKS_AT'
	`, internalID)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var company CompanyNode
			rows.Scan(&company.Name, &company.Position, &company.IsCurrent)
			candidate.Companies = append(candidate.Companies, company)
		}
	}

	// Fetch education
	rows, _ = s.db.QueryContext(ctx, `
		SELECT e.properties->>'institution', COALESCE(e.properties->>'degree', ''), 
		       COALESCE(e.properties->>'field', '')
		FROM graph_edges ed
		JOIN graph_nodes e ON ed.target_node_id = e.id
		WHERE ed.source_node_id = $1 AND ed.edge_type = 'GRADUATED_FROM'
	`, internalID)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var edu EducationNode
			rows.Scan(&edu.Institution, &edu.Degree, &edu.Field)
			candidate.Education = append(candidate.Education, edu)
		}
	}
}

// generateEnhancedSummary creates summary with community insights
func (s *EnhancedSearchEngine) generateEnhancedSummary(
	query string,
	candidates []LLMRankedCandidate,
	communities []CommunityInsight,
	reasoning string,
) string {
	if len(candidates) == 0 {
		return "No candidates found matching your query."
	}

	excellentCount := 0
	for _, c := range candidates {
		if c.Fit == "excellent" {
			excellentCount++
		}
	}

	summary := fmt.Sprintf("Found %d relevant candidates", len(candidates))
	if excellentCount > 0 {
		summary += fmt.Sprintf(" (%d excellent match", excellentCount)
		if excellentCount > 1 {
			summary += "es"
		}
		summary += ")"
	}
	summary += ". "

	if len(communities) > 0 {
		summary += fmt.Sprintf("Insights from %d related communities in the knowledge graph. ", len(communities))
	}

	if reasoning != "" {
		summary += reasoning
	}

	return summary
}

func buildCandidateProfiles(candidates []CandidateResult) string {
	var builder strings.Builder

	for i, candidate := range candidates {
		builder.WriteString(fmt.Sprintf("\n%d. %s\n", i+1, candidate.Name))

		if candidate.CurrentPosition != "" {
			builder.WriteString(fmt.Sprintf("   Position: %s", candidate.CurrentPosition))
			if candidate.Seniority != "" {
				builder.WriteString(fmt.Sprintf(" (%s)", candidate.Seniority))
			}
			builder.WriteString("\n")
		}

		if candidate.TotalExperience != nil {
			builder.WriteString(fmt.Sprintf("   Experience: %v years\n", candidate.TotalExperience))
		}

		if len(candidate.Skills) > 0 {
			skillNames := make([]string, 0, len(candidate.Skills))
			for _, skill := range candidate.Skills {
				if skill.Proficiency != "" {
					skillNames = append(skillNames, fmt.Sprintf("%s (%s)", skill.Name, skill.Proficiency))
				} else {
					skillNames = append(skillNames, skill.Name)
				}
			}
			builder.WriteString(fmt.Sprintf("   Skills: %s\n", strings.Join(skillNames, ", ")))
		}

		if len(candidate.Companies) > 0 {
			companyNames := make([]string, 0, len(candidate.Companies))
			for _, company := range candidate.Companies {
				if company.IsCurrent {
					companyNames = append(companyNames, fmt.Sprintf("%s (Current)", company.Name))
				} else {
					companyNames = append(companyNames, company.Name)
				}
			}
			builder.WriteString(fmt.Sprintf("   Companies: %s\n", strings.Join(companyNames, ", ")))
		}
	}

	return builder.String()
}
