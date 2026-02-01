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

// LLMSearchEngine performs semantic search using LLM reasoning instead of manual scoring
type LLMSearchEngine struct {
	db  *sql.DB
	llm LLMClient
}

// DEPRECATED: fitToScore and adjustedLocalScore are NO LONGER USED
// All scoring is now done by pure LLM in hybrid_search.go -> LLMScorer
// These functions remain for backward compatibility but should not be called

func NewLLMSearchEngine(db *sql.DB, llm LLMClient) *LLMSearchEngine {
	return &LLMSearchEngine{
		db:  db,
		llm: llm,
	}
}

// LLMSearchResult represents a candidate ranked by LLM
type LLMSearchResult struct {
	Query      string               `json:"query"`
	Candidates []LLMRankedCandidate `json:"candidates"`
	Summary    string               `json:"summary"`
	TotalFound int                  `json:"total_found"`
	Reasoning  string               `json:"reasoning"`
}

// LLMRankedCandidate is a candidate with LLM-generated ranking and reasoning
type LLMRankedCandidate struct {
	CVID            int             `json:"cv_id"`
	PersonID        string          `json:"person_id"`
	Name            string          `json:"name"`
	CurrentPosition string          `json:"current_position"`
	Seniority       string          `json:"seniority"`
	TotalExperience interface{}     `json:"total_experience_years"`
	Skills          []SkillNode     `json:"skills"`
	Companies       []CompanyNode   `json:"companies"`
	Education       []EducationNode `json:"education"`
	Fit             string          `json:"fit"` // excellent, good, fair, poor
	Reasoning       string          `json:"reasoning"`
	KeyStrengths    []string        `json:"key_strengths"`
	MatchScore      float64         `json:"match_score"`
	FinalScore      float64         `json:"final_score"`
}

// Search performs LLM-based semantic search
func (s *LLMSearchEngine) Search(ctx context.Context, query string) (*LLMSearchResult, error) {
	log.Printf("[LLM Search] Starting semantic search for: %s", query)

	// Step 1: Fetch ALL candidates from database (no SQL filtering)
	allCandidates, err := s.fetchAllCandidates(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch candidates: %w", err)
	}

	log.Printf("[LLM Search] Loaded %d total candidates from database", len(allCandidates))

	if len(allCandidates) == 0 {
		return &LLMSearchResult{
			Query:      query,
			Candidates: []LLMRankedCandidate{},
			Summary:    "No candidates found in database.",
			TotalFound: 0,
		}, nil
	}

	// Step 2: LLM analyzes and ranks candidates
	rankedCandidates, reasoning, err := s.llmRankCandidates(ctx, query, allCandidates)
	if err != nil {
		return nil, fmt.Errorf("LLM ranking failed: %w", err)
	}

	// Step 3: Generate summary
	summary := s.generateSummary(query, rankedCandidates, reasoning)

	return &LLMSearchResult{
		Query:      query,
		Candidates: rankedCandidates,
		Summary:    summary,
		TotalFound: len(rankedCandidates),
		Reasoning:  reasoning,
	}, nil
}

// fetchAllCandidates loads all candidates from database (no filtering)
func (s *LLMSearchEngine) fetchAllCandidates(ctx context.Context) ([]CandidateResult, error) {
	query := `
		SELECT DISTINCT p.node_id, p.properties
		FROM graph_nodes p
		WHERE p.node_type = 'person'
		ORDER BY p.node_id
	`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []CandidateResult
	for rows.Next() {
		var result CandidateResult
		var propsJSON []byte

		if err := rows.Scan(&result.PersonID, &propsJSON); err != nil {
			log.Printf("[LLM Search] Scan error: %v", err)
			continue
		}

		// Parse properties
		var props map[string]interface{}
		if err := json.Unmarshal(propsJSON, &props); err != nil {
			log.Printf("[LLM Search] JSON unmarshal error: %v", err)
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

		// Fetch related nodes (skills, companies, education)
		s.enrichCandidate(ctx, &result)

		candidates = append(candidates, result)
	}

	return candidates, nil
}

// enrichCandidate fetches skills, companies, education for a candidate
func (s *LLMSearchEngine) enrichCandidate(ctx context.Context, candidate *CandidateResult) {
	// First, get the internal ID from node_id
	var internalID int
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM graph_nodes WHERE node_id = $1`,
		candidate.PersonID,
	).Scan(&internalID)

	if err != nil {
		log.Printf("[LLM Search] Failed to get internal ID for %s (node_id=%s): %v",
			candidate.Name, candidate.PersonID, err)
		return
	}

	// Fetch skills
	skillQuery := `
		SELECT s.properties->>'name' as skill_name,
		       COALESCE(s.properties->>'proficiency', '') as proficiency
		FROM graph_edges e
		JOIN graph_nodes s ON e.target_node_id = s.id
		WHERE e.source_node_id = $1
		  AND e.edge_type = 'HAS_SKILL'
		  AND s.node_type = 'skill'
	`
	rows, err := s.db.QueryContext(ctx, skillQuery, internalID)
	if err != nil {
		log.Printf("[LLM Search] Skill query error for %s: %v", candidate.Name, err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var skill SkillNode
			if err := rows.Scan(&skill.Name, &skill.Proficiency); err == nil {
				candidate.Skills = append(candidate.Skills, skill)
			}
		}
	}

	// Fetch companies
	companyQuery := `
		SELECT c.properties->>'name' as company_name,
		       COALESCE(c.properties->>'position', '') as position,
		       COALESCE((c.properties->>'is_current')::boolean, false) as is_current
		FROM graph_edges e
		JOIN graph_nodes c ON e.target_node_id = c.id
		WHERE e.source_node_id = $1
		  AND e.edge_type = 'WORKS_AT'
		  AND c.node_type = 'company'
	`
	rows, err = s.db.QueryContext(ctx, companyQuery, internalID)
	if err != nil {
		log.Printf("[LLM Search] Company query error for %s: %v", candidate.Name, err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var company CompanyNode
			if err := rows.Scan(&company.Name, &company.Position, &company.IsCurrent); err == nil {
				candidate.Companies = append(candidate.Companies, company)
			}
		}
	}

	// Fetch education
	eduQuery := `
		SELECT e.properties->>'institution' as institution,
		       COALESCE(e.properties->>'degree', '') as degree,
		       COALESCE(e.properties->>'field', '') as field
		FROM graph_edges ed
		JOIN graph_nodes e ON ed.target_node_id = e.id
		WHERE ed.source_node_id = $1
		  AND ed.edge_type = 'GRADUATED_FROM'
		  AND e.node_type = 'education'
	`
	rows, err = s.db.QueryContext(ctx, eduQuery, internalID)
	if err != nil {
		log.Printf("[LLM Search] Education query error for %s: %v", candidate.Name, err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var edu EducationNode
			if err := rows.Scan(&edu.Institution, &edu.Degree, &edu.Field); err == nil {
				candidate.Education = append(candidate.Education, edu)
			}
		}
	}
}

// llmRankCandidates uses LLM to analyze and rank candidates semantically
func (s *LLMSearchEngine) llmRankCandidates(ctx context.Context, query string, candidates []CandidateResult) ([]LLMRankedCandidate, string, error) {
	// Build candidate profiles for LLM
	candidateProfiles := s.buildCandidateProfiles(candidates)

	prompt := fmt.Sprintf(`You are an expert technical recruiter with deep knowledge of software engineering roles, skills, and career progression.

USER QUERY: "%s"

CANDIDATE DATABASE (%d candidates):

%s

TASK:
Analyze each candidate and determine who best matches the user's query. Consider:
1. Semantic skill matching (e.g., "K8s" = "Kubernetes", "React" relevant for "Frontend Developer")
2. Domain expertise (e.g., "Bank" matches "ING Bank", "Akbank")
3. Position alignment (e.g., "Developer" matches "Software Engineer", "Tech Lead")
4. Experience level and seniority
5. Career trajectory and growth
6. Contextual relevance (weight factors based on what the query emphasizes)

IMPORTANT GUIDELINES:
- Do NOT use arbitrary numeric scores
- Evaluate holistically based on the query's intent
- Consider partial matches and related skills
- If query asks about "banking", prioritize banking domain experience
- If query asks about specific technology, prioritize that skill
- Give credit for related/complementary skills

OUTPUT JSON (return ONLY valid JSON, no markdown):
{
  "top_matches": [
    {
      "name": "Candidate Name",
      "fit": "excellent|good|fair|poor",
      "reasoning": "Detailed explanation of why this candidate matches (2-3 sentences)",
      "key_strengths": ["Strength 1", "Strength 2", "Strength 3"]
    }
  ],
  "overall_reasoning": "1-2 sentences explaining the matching logic and any patterns observed"
}

Return candidates sorted by relevance (best matches first). Include only candidates with "excellent" or "good" fit.
`, query, len(candidates), candidateProfiles)

	log.Printf("[LLM Search] Sending %d candidates to LLM for analysis", len(candidates))

	response, err := s.llm.Generate(prompt)
	if err != nil {
		return nil, "", fmt.Errorf("LLM generation failed: %w", err)
	}

	// Parse LLM response
	response = strings.TrimSpace(response)
	// Remove markdown code blocks if present
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
		log.Printf("[LLM Search] Failed to parse LLM response: %v", err)
		log.Printf("[LLM Search] Response was: %s", response)
		return nil, "", fmt.Errorf("failed to parse LLM response: %w", err)
	}

	// NO MORE LOCAL SCORING! Pure LLM approach
	// Map LLM results back to full candidate objects
	var rankedCandidates []LLMRankedCandidate
	for _, match := range llmResult.TopMatches {
		// Find the candidate in original list
		for _, candidate := range candidates {
			if candidate.Name == match.Name {
				// Use LLM fit directly as score (no more local heuristics!)
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
					FinalScore:      llmScore, // Pure LLM score (no local heuristics)
				})
				break
			}
		}
	}

	// Sort by FinalScore descending (pure LLM ranking)
	sort.Slice(rankedCandidates, func(i, j int) bool {
		return rankedCandidates[i].FinalScore > rankedCandidates[j].FinalScore
	})

	log.Printf("[LLM Search] LLM ranked %d candidates as relevant", len(rankedCandidates))

	return rankedCandidates, llmResult.OverallReasoning, nil
}

// buildCandidateProfiles creates a compact text representation of candidates for LLM
func (s *LLMSearchEngine) buildCandidateProfiles(candidates []CandidateResult) string {
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

		if len(candidate.Education) > 0 {
			eduNames := make([]string, 0, len(candidate.Education))
			for _, edu := range candidate.Education {
				if edu.Degree != "" && edu.Institution != "" {
					eduNames = append(eduNames, fmt.Sprintf("%s from %s", edu.Degree, edu.Institution))
				} else if edu.Institution != "" {
					eduNames = append(eduNames, edu.Institution)
				}
			}
			if len(eduNames) > 0 {
				builder.WriteString(fmt.Sprintf("   Education: %s\n", strings.Join(eduNames, ", ")))
			}
		}
	}

	return builder.String()
}

// generateSummary creates a natural language summary of search results
func (s *LLMSearchEngine) generateSummary(query string, candidates []LLMRankedCandidate, reasoning string) string {
	if len(candidates) == 0 {
		return "No candidates found matching your query."
	}

	excellentCount := 0
	goodCount := 0
	for _, c := range candidates {
		if c.Fit == "excellent" {
			excellentCount++
		} else if c.Fit == "good" {
			goodCount++
		}
	}

	summary := fmt.Sprintf("Found %d relevant candidates", len(candidates))
	if excellentCount > 0 {
		summary += fmt.Sprintf(" (%d excellent match", excellentCount)
		if excellentCount > 1 {
			summary += "es"
		}
		if goodCount > 0 {
			summary += fmt.Sprintf(", %d good match", goodCount)
			if goodCount > 1 {
				summary += "es"
			}
		}
		summary += ")"
	}
	summary += ". "

	if reasoning != "" {
		summary += reasoning
	}

	return summary
}
