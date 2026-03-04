package graphrag

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// LLMScorer performs pure LLM-based candidate scoring
// No local heuristics, only LLM intelligence
type LLMScorer struct {
	llm   LLMClient
	cache *LLMCache
}

func NewLLMScorer(llm LLMClient) *LLMScorer {
	return &LLMScorer{
		llm:   llm,
		cache: NewLLMCache(30 * time.Minute), // Match semantic cache TTL
	}
}

// CandidateScore represents LLM's evaluation of a candidate
type CandidateScore struct {
	PersonID   string   `json:"person_id"`
	Score      float64  `json:"score"`      // 0-100 (100 = perfect match)
	Confidence float64  `json:"confidence"` // 0-1 (how confident is LLM)
	Reasoning  string   `json:"reasoning"`  // Why this score?
	Evidence   []string `json:"evidence"`   // Key facts supporting the score
	Fit        string   `json:"fit"`        // excellent/good/fair/poor
}

// LLMScoreResponse is the structured response from LLM
type LLMScoreResponse struct {
	Candidates []CandidateScore `json:"candidates"`
	Summary    string           `json:"summary"`
}

const llmBatchSize = 8 // single call; skill searches may send more, skill-less capped at FinalTopN=8

// ScoreCandidates sends candidates to LLM for scoring using parallel batches.
// communitySummaries contains LLM-generated summaries of the most relevant graph communities
// for this query — used as global context (GraphRAG global search style).
// Returns scored and sorted candidates.
func (s *LLMScorer) ScoreCandidates(ctx context.Context, query string, candidates []FusedCandidate, communitySummaries []string) ([]CandidateScore, error) {
	if len(candidates) == 0 {
		return []CandidateScore{}, nil
	}

	// Check cache first
	candidateIDs := make([]string, len(candidates))
	for i, c := range candidates {
		candidateIDs[i] = c.PersonID
	}

	if cachedScores, found := s.cache.Get(query, candidateIDs); found {
		log.Printf("[LLMScorer] Cache HIT for query: %s (%d candidates)", query, len(cachedScores))
		return cachedScores, nil
	}

	log.Printf("[LLMScorer] Scoring %d candidates in a single call for consistent ranking", len(candidates))

	prompt := s.buildScoringPrompt(query, candidates, communitySummaries)
	response, err := s.llm.Generate(prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM scoring call failed: %w", err)
	}
	parsed, err := s.parseScoreResponse(response)
	if err != nil {
		return nil, fmt.Errorf("LLM score parse failed: %w", err)
	}
	allScores := parsed.Candidates

	if len(allScores) == 0 {
		return nil, fmt.Errorf("LLM returned no scores")
	}

	log.Printf("[LLMScorer] Successfully scored %d candidates in a single call", len(allScores))

	// Cache the combined results
	s.cache.Set(query, candidateIDs, allScores)

	return allScores, nil
}

// buildScoringPrompt creates a lean, HR-focused ranking prompt.
// No hardcoded role rules — LLM evaluates fit based on skills, title, and experience.
func (s *LLMScorer) buildScoringPrompt(query string, candidates []FusedCandidate, _ []string) string {
	var b strings.Builder

	b.WriteString("You are a senior technical recruiter. Score each candidate for the following role.\n\n")
	b.WriteString("Role: " + query + "\n\n")
	b.WriteString("Scoring (0-100):\n")
	b.WriteString("- Skill depth: years of experience and proficiency level in the required skill (most important)\n")
	b.WriteString("- Skills breadth: does their overall tech stack align with the role?\n")
	b.WriteString("- Title match: does their current or past role reflect the position? (secondary — deep skill experience outweighs a matching title alone)\n\n")
	b.WriteString("Candidates:\n")

	for i, c := range candidates {
		b.WriteString(fmt.Sprintf("\n[%d] person_id: %s\n", i+1, c.PersonID))
		b.WriteString(fmt.Sprintf("  Title: %s | Seniority: %s | Experience: %d yrs\n",
			c.CurrentPosition, c.Seniority, c.TotalExperienceYears))
		b.WriteString(fmt.Sprintf("  Skills: %s\n", skillNames(c.Skills)))
		b.WriteString(fmt.Sprintf("  Work history: %s\n", companyNames(c.Companies)))
	}

	b.WriteString(`
Return ONLY valid JSON, no markdown:
{
  "candidates": [
    {
      "person_id": "person_xxx",
      "score": 85,
      "confidence": 0.9,
      "reasoning": "One sentence explanation.",
      "evidence": ["key fact 1", "key fact 2"],
      "fit": "excellent"
    }
  ],
  "summary": "One sentence overall summary."
}
fit values: excellent (80+) / good (60-79) / fair (40-59) / poor (<40)
Score ALL candidates. Return ONLY JSON.
`)

	return b.String()
}

// parseScoreResponse extracts structured scores from LLM response
func (s *LLMScorer) parseScoreResponse(response string) (*LLMScoreResponse, error) {
	// Try to extract JSON from response
	jsonStr := extractJSON(response)
	if jsonStr == "" {
		return nil, fmt.Errorf("no valid JSON found in LLM response")
	}

	var scoreResponse LLMScoreResponse
	if err := json.Unmarshal([]byte(jsonStr), &scoreResponse); err != nil {
		log.Printf("[LLMScorer] Failed to parse JSON: %v\nResponse: %s", err, jsonStr)
		return nil, fmt.Errorf("json parse error: %w", err)
	}

	// Validate scores
	for i := range scoreResponse.Candidates {
		c := &scoreResponse.Candidates[i]

		// Clamp score to 0-100
		if c.Score < 0 {
			c.Score = 0
		} else if c.Score > 100 {
			c.Score = 100
		}

		// Clamp confidence to 0-1
		if c.Confidence < 0 {
			c.Confidence = 0
		} else if c.Confidence > 1 {
			c.Confidence = 1
		}

		// Set default fit if empty
		if c.Fit == "" {
			if c.Score >= 75 {
				c.Fit = "excellent"
			} else if c.Score >= 60 {
				c.Fit = "good"
			} else if c.Score >= 40 {
				c.Fit = "fair"
			} else {
				c.Fit = "poor"
			}
		}
	}

	return &scoreResponse, nil
}

// extractJSON finds and extracts JSON object from text
// Handles cases where LLM adds markdown or extra text
func extractJSON(text string) string {
	// Try to find JSON object boundaries
	start := -1
	end := -1
	braceCount := 0

	for i, char := range text {
		if char == '{' {
			if start == -1 {
				start = i
			}
			braceCount++
		} else if char == '}' {
			braceCount--
			if braceCount == 0 && start != -1 {
				end = i + 1
				break
			}
		}
	}

	if start != -1 && end != -1 {
		return text[start:end]
	}

	return ""
}

// skillNames extracts skill names from SkillNode array
func skillNames(skills []SkillNode) string {
	if len(skills) == 0 {
		return "None listed"
	}

	// Sort by proficiency (Expert first) then years of experience desc
	// Unknown/empty proficiency ranks last (4)
	sorted := make([]SkillNode, len(skills))
	copy(sorted, skills)
	profRank := func(p string) int {
		switch p {
		case "Expert":
			return 0
		case "Advanced":
			return 1
		case "Intermediate":
			return 2
		case "Beginner":
			return 3
		default:
			return 4 // empty or unknown → lowest priority
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		pi := profRank(sorted[i].Proficiency)
		pj := profRank(sorted[j].Proficiency)
		if pi != pj {
			return pi < pj
		}
		return sorted[i].YearsOfExperience > sorted[j].YearsOfExperience
	})

	// Cap at 8 most relevant skills to keep prompt concise
	if len(sorted) > 8 {
		sorted = sorted[:8]
	}

	names := make([]string, len(sorted))
	for i, s := range sorted {
		if s.Proficiency != "" && s.YearsOfExperience > 0 {
			names[i] = fmt.Sprintf("%s (%s, %d yrs)", s.Name, s.Proficiency, s.YearsOfExperience)
		} else if s.Proficiency != "" {
			names[i] = fmt.Sprintf("%s (%s)", s.Name, s.Proficiency)
		} else {
			names[i] = s.Name
		}
	}
	return strings.Join(names, ", ")
}

// companyNames extracts company names from CompanyNode array
func companyNames(companies []CompanyNode) string {
	if len(companies) == 0 {
		return "None listed"
	}
	names := make([]string, len(companies))
	for i, c := range companies {
		entry := c.Name
		if c.Position != "" {
			entry = fmt.Sprintf("%s (%s)", c.Name, c.Position)
		}
		if c.IsCurrent {
			entry += " [Current]"
		}
		names[i] = entry
	}
	return strings.Join(names, ", ")
}
