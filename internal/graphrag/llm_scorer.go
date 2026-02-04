package graphrag

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
		cache: NewLLMCache(5 * time.Minute), // Cache for 5 minutes
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

// ScoreCandidates sends candidates to LLM for scoring
// Returns scored and sorted candidates
func (s *LLMScorer) ScoreCandidates(ctx context.Context, query string, candidates []FusedCandidate) ([]CandidateScore, error) {
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

	log.Printf("[LLMScorer] Cache MISS - Scoring %d candidates for query: %s", len(candidates), query)

	// Build prompt with candidate features
	prompt := s.buildScoringPrompt(query, candidates)

	// Call LLM
	response, err := s.llm.Generate(prompt)
	if err != nil {
		return nil, fmt.Errorf("llm scoring failed: %w", err)
	}

	// Parse structured response
	scores, err := s.parseScoreResponse(response)
	if err != nil {
		return nil, fmt.Errorf("failed to parse llm scores: %w", err)
	}

	log.Printf("[LLMScorer] Successfully scored %d candidates", len(scores.Candidates))

	// Cache the results
	s.cache.Set(query, candidateIDs, scores.Candidates)

	return scores.Candidates, nil
}

// buildScoringPrompt creates a detailed prompt for LLM scoring
func (s *LLMScorer) buildScoringPrompt(query string, candidates []FusedCandidate) string {
	prompt := fmt.Sprintf(`You are an expert technical recruiter. Score each candidate for this job query.

**Job Query:** %s

**Your Task:**
1. Evaluate each candidate's match quality (0-100 score)
2. Provide confidence level (0-1)
3. Explain your reasoning with specific details (IMPORTANT: mention years of experience for key skills)
4. List key evidence (skills with years, experience, etc.)
5. Assign fit level: excellent/good/fair/poor

**Scoring Guidelines:**
- 90-100: Perfect match - Community match + senior experience (8+ years) in domain
- 80-89: Strong match - Community match + solid experience (5-7 years)
- 70-79: Good match - Community match + junior/mid experience (3-4 years) OR direct job title match with less community fit
- 60-69: Acceptable match - Community match + very junior (1-2 years) OR related position without community match
- 40-59: Fair match - Some overlap in skills/experience but weak community fit
- 0-39: Poor match - Wrong domain and insufficient skills

**Evaluation Criteria (in order of importance):**
1. **Community Match:** Is the candidate in the right professional community for this role?
2. **Years of Experience:** How much relevant experience do they have? (THIS IS THE PRIMARY DIFFERENTIATOR when communities match!)
3. **Job Title Match:** Does their current position match what's being searched?
4. **Skill Relevance:** Do their skills match the job requirements?

**CRITICAL SCORING RULES:**
- **When candidates are in the SAME community, ALWAYS give higher score to the one with MORE years of experience**
- Experience should create clear score separation (e.g., 12 yrs = 85-90, 4 yrs = 70-75, 3 yrs = 65-70)
- Job title match is SECONDARY to experience when community matches
- Example 1: "Product Lead" (analyst, 12 yrs) should score 85-90, "Business Analyst" (analyst, 4 yrs) should score 70-75
- Example 2: "Product Owner & BA" (analyst, 3 yrs) should score 65-70, less than someone with 4+ years in same community

**IMPORTANT:** Skills are listed with proficiency levels and years of experience (e.g., "Java (Expert, 13 yrs)"). 
Pay close attention to CURRENT POSITION, COMMUNITY MEMBERSHIP, and years of experience when scoring.

**Community Definitions:**
- "analyst": Business Analysts, Product Owners, Product Managers, Data Analysts
- "backend": Backend Developers, API Engineers, Microservices Engineers
- "frontend": Frontend Developers, UI Engineers, React/Vue developers
- "mobile": iOS/Android developers, React Native developers
- "data": Data Engineers, Data Scientists, ML Engineers
- "devops": DevOps Engineers, SRE, Infrastructure Engineers
- "qa-test": QA Engineers, Test Automation Engineers

**Candidates:**
`, query)

	// Add candidate details
	for i, c := range candidates {
		skills := skillNames(c.Skills)
		companies := companyNames(c.Companies)

		// Format community scores
		communityInfo := "No strong community match"
		if c.Community != "" {
			if len(c.Communities) > 0 {
				communityInfo = fmt.Sprintf("Primary: %s, All communities: %v", c.Community, c.Communities)
			} else {
				communityInfo = fmt.Sprintf("Primary: %s", c.Community)
			}
		}

		prompt += fmt.Sprintf(`
---
Candidate %d:
- Person ID: %s
- Name: %s
- **CURRENT POSITION: %s** (This is their actual job title - very important for matching!)
- **COMMUNITY MEMBERSHIP: %s** (Shows their professional domain - VERY important!)
- Seniority: %s (%d years total experience)
- Top Skills: %v
- Work History: %v
- Fusion Score: %.2f (Pre-LLM ranking: #%d)

`, i+1, c.PersonID, c.Name, c.CurrentPosition, communityInfo, c.Seniority, c.TotalExperienceYears,
			skills, companies,
			c.FusionScore, c.Rank)
	}

	prompt += `
**Response Format (JSON):**
{
  "candidates": [
    {
      "person_id": "person_xxx",
      "score": 85.5,
      "confidence": 0.9,
      "reasoning": "Strong backend experience with Go and microservices. 5+ years in fintech.",
      "evidence": ["Go expert", "Worked at major bank", "Led migration to microservices"],
      "fit": "excellent"
    }
  ],
  "summary": "Found 3 strong candidates with relevant banking and Go experience."
}

**Important:**
- Score ALL candidates in the list
- Be objective and evidence-based
- Consider: technical skills, experience relevance, seniority match
- Return ONLY valid JSON, no markdown formatting
`

	return prompt
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
	names := make([]string, len(skills))
	for i, s := range skills {
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
		if c.IsCurrent {
			names[i] = fmt.Sprintf("%s (Current)", c.Name)
		} else {
			names[i] = c.Name
		}
	}
	return strings.Join(names, ", ")
}
