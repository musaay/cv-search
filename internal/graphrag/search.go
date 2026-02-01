package graphrag

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// SearchEngine combines query analysis, graph querying, and LLM enhancement
type SearchEngine struct {
	analyzer *QueryAnalyzer
	querier  *GraphQuerier
	llm      LLMClient
}

func NewSearchEngine(db *sql.DB, llm LLMClient) *SearchEngine {
	return &SearchEngine{
		analyzer: NewQueryAnalyzer(llm),
		querier:  NewGraphQuerier(db),
		llm:      llm,
	}
}

// SearchResult represents the complete GraphRAG search result
type SearchResult struct {
	Query          string            `json:"query"`
	ParsedCriteria *SearchCriteria   `json:"parsed_criteria"`
	Candidates     []CandidateResult `json:"candidates"`
	Summary        string            `json:"summary"` // LLM-generated summary
	TotalFound     int               `json:"total_found"`
	ProcessingTime string            `json:"processing_time"`
}

// Search performs end-to-end GraphRAG search
func (s *SearchEngine) Search(ctx context.Context, naturalLanguageQuery string) (*SearchResult, error) {
	log.Printf("[GraphRAG Search] Starting search for: %s", naturalLanguageQuery)

	result := &SearchResult{
		Query: naturalLanguageQuery,
	}

	// Step 1: Analyze query with LLM
	criteria, err := s.analyzer.AnalyzeQuery(ctx, naturalLanguageQuery)
	if err != nil {
		return nil, fmt.Errorf("query analysis failed: %w", err)
	}
	result.ParsedCriteria = criteria

	// Step 2: Query graph
	candidates, err := s.querier.QueryGraph(ctx, criteria)
	if err != nil {
		return nil, fmt.Errorf("graph query failed: %w", err)
	}

	// Step 2.5: Let the LLM post-filter / rerank candidates based on the original
	// natural language query and the extracted criteria. This centralizes position
	// and intent-sensitive filtering in the LLM instead of sprinkling ad-hoc checks
	// inside the SQL querier.
	filtered, ferr := s.analyzer.FilterCandidatesWithLLM(ctx, naturalLanguageQuery, criteria, candidates)
	if ferr != nil {
		log.Printf("[GraphRAG] LLM post-filter failed, using original candidates: %v", ferr)
	} else {
		candidates = filtered
	}

	result.Candidates = candidates
	result.TotalFound = len(candidates)

	// Step 3: Generate LLM summary
	if len(candidates) > 0 {
		summary, err := s.generateSummary(ctx, naturalLanguageQuery, candidates)
		if err != nil {
			log.Printf("[GraphRAG Search] Summary generation failed: %v", err)
			result.Summary = fmt.Sprintf("Found %d matching candidates", len(candidates))
		} else {
			result.Summary = summary
		}
	} else {
		result.Summary = "No candidates found matching your criteria."
	}

	log.Printf("[GraphRAG Search] Search complete: %d candidates found", len(candidates))
	return result, nil
}

func (s *SearchEngine) generateSummary(ctx context.Context, query string, candidates []CandidateResult) (string, error) {
	// Build context from top candidates (max 5)
	topCandidates := candidates
	if len(topCandidates) > 5 {
		topCandidates = candidates[:5]
	}

	contextBuilder := strings.Builder{}
	contextBuilder.WriteString("Found the following candidates:\n\n")

	for i, candidate := range topCandidates {
		contextBuilder.WriteString(fmt.Sprintf("%d. %s\n", i+1, candidate.Name))
		contextBuilder.WriteString(fmt.Sprintf("   Position: %s (%s)\n", candidate.CurrentPosition, candidate.Seniority))

		if len(candidate.Skills) > 0 {
			skillNames := []string{}
			for _, skill := range candidate.Skills {
				skillNames = append(skillNames, skill.Name)
			}
			contextBuilder.WriteString(fmt.Sprintf("   Skills: %s\n", strings.Join(skillNames, ", ")))
		}

		if len(candidate.Companies) > 0 {
			companyNames := []string{}
			for _, company := range candidate.Companies {
				companyNames = append(companyNames, company.Name)
			}
			contextBuilder.WriteString(fmt.Sprintf("   Companies: %s\n", strings.Join(companyNames, ", ")))
		}

		contextBuilder.WriteString(fmt.Sprintf("   Match Score: %.1f/100\n", candidate.MatchScore))
		if len(candidate.MatchReasons) > 0 {
			contextBuilder.WriteString(fmt.Sprintf("   Why: %s\n", strings.Join(candidate.MatchReasons, ", ")))
		}
		contextBuilder.WriteString("\n")
	}

	prompt := fmt.Sprintf(`You are a technical recruiter. Based on the candidate data below, provide a brief summary answering the user's question.

User Question: "%s"

Candidate Data:
%s

Provide a concise 2-3 sentence summary highlighting:
1. How many candidates match
2. Key qualifications of top candidates
3. Any notable patterns or standout candidates

Summary:`, query, contextBuilder.String())

	summary, err := s.llm.Generate(prompt)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(summary), nil
}
