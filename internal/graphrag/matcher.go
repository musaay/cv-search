package graphrag

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
)

type CriteriaMatcher struct {
	db        *sql.DB
	llmClient LLMClient
}

type SearchCriteria struct {
	// GraphRAG query fields
	Skills        []string `json:"skills"`         // All skills (required + preferred combined)
	Companies     []string `json:"companies"`      // Company names
	Positions     []string `json:"positions"`      // Job titles
	Seniority     string   `json:"seniority"`      // Junior|Mid-level|Senior|Lead|Architect
	Education     []string `json:"education"`      // Institution or degree
	MinExperience *int     `json:"min_experience"` // Minimum years
	MaxExperience *int     `json:"max_experience"` // Maximum years
	Location      []string `json:"location"`       // Cities/countries

	// Legacy fields (backward compatibility)
	RequiredSkills  []string               `json:"required_skills,omitempty"`
	PreferredSkills []string               `json:"preferred_skills,omitempty"`
	Weights         ScoringWeights         `json:"weights,omitempty"`
	CustomFilters   map[string]interface{} `json:"custom_filters,omitempty"`
}

type ScoringWeights struct {
	SkillWeight      float64 `json:"skill_weight"`      // default: 0.4
	ExperienceWeight float64 `json:"experience_weight"` // default: 0.3
	LocationWeight   float64 `json:"location_weight"`   // default: 0.15
	EducationWeight  float64 `json:"education_weight"`  // default: 0.15
}

type ScoredCandidate struct {
	CandidateID     int                    `json:"candidate_id"`
	Name            string                 `json:"name"`
	Email           string                 `json:"email"`
	TotalScore      float64                `json:"total_score"`
	SkillScore      float64                `json:"skill_score"`
	ExperienceScore float64                `json:"experience_score"`
	LocationScore   float64                `json:"location_score"`
	EducationScore  float64                `json:"education_score"`
	Rank            int                    `json:"rank"`
	MatchDetails    map[string]interface{} `json:"match_details"`
}

func NewCriteriaMatcher(db *sql.DB, llmClient LLMClient) *CriteriaMatcher {
	return &CriteriaMatcher{
		db:        db,
		llmClient: llmClient,
	}
}

// MatchAndRank finds and ranks candidates based on criteria
func (m *CriteriaMatcher) MatchAndRank(ctx context.Context, criteria SearchCriteria) ([]ScoredCandidate, error) {
	// Set default weights if not provided
	if criteria.Weights.SkillWeight == 0 {
		criteria.Weights = ScoringWeights{
			SkillWeight:      0.4,
			ExperienceWeight: 0.3,
			LocationWeight:   0.15,
			EducationWeight:  0.15,
		}
	}

	// 1. Get all candidates
	candidates, err := m.fetchAllCandidates(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch candidates: %w", err)
	}

	// 2. Score each candidate
	scored := []ScoredCandidate{}
	queryID := uuid.New().String()

	for _, candidate := range candidates {
		score := m.scoreCandidate(ctx, candidate, criteria)
		score.Rank = 0 // Will be set after sorting

		scored = append(scored, score)

		// Save score to database
		if err := m.saveCandidateScore(ctx, queryID, criteria, score); err != nil {
			// Log error but continue
			fmt.Printf("failed to save score for candidate %d: %v\n", candidate.ID, err)
		}
	}

	// 3. Sort by total score (descending)
	sortByScore(scored)

	// 4. Assign ranks
	for i := range scored {
		scored[i].Rank = i + 1
	}

	return scored, nil
}

type candidateData struct {
	ID         int
	Name       string
	Email      string
	Experience string
	Skills     string
	Location   string
	CVText     string
}

func (m *CriteriaMatcher) fetchAllCandidates(ctx context.Context) ([]candidateData, error) {
	query := `
		SELECT 
			c.id, c.name, c.email, c.experience, c.skills, c.location,
			COALESCE(cv.parsed_text, '') as cv_text
		FROM candidates c
		LEFT JOIN cv_files cv ON c.id = cv.candidate_id
	`

	rows, err := m.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []candidateData
	for rows.Next() {
		var c candidateData
		if err := rows.Scan(&c.ID, &c.Name, &c.Email, &c.Experience, &c.Skills, &c.Location, &c.CVText); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}

	return candidates, rows.Err()
}

func (m *CriteriaMatcher) scoreCandidate(ctx context.Context, candidate candidateData, criteria SearchCriteria) ScoredCandidate {
	// Calculate individual scores
	skillScore := m.calculateSkillScore(candidate, criteria)
	expScore := m.calculateExperienceScore(candidate, criteria)
	locScore := m.calculateLocationScore(candidate, criteria)
	eduScore := m.calculateEducationScore(candidate, criteria)

	// Calculate weighted total score
	totalScore := (skillScore * criteria.Weights.SkillWeight) +
		(expScore * criteria.Weights.ExperienceWeight) +
		(locScore * criteria.Weights.LocationWeight) +
		(eduScore * criteria.Weights.EducationWeight)

	// Check location match (handle array)
	locationMatch := false
	for _, loc := range criteria.Location {
		if strings.EqualFold(candidate.Location, loc) {
			locationMatch = true
			break
		}
	}

	matchDetails := map[string]interface{}{
		"matched_required_skills":  m.getMatchedSkills(candidate, criteria.RequiredSkills),
		"matched_preferred_skills": m.getMatchedSkills(candidate, criteria.PreferredSkills),
		"location_match":           locationMatch,
	}

	return ScoredCandidate{
		CandidateID:     candidate.ID,
		Name:            candidate.Name,
		Email:           candidate.Email,
		TotalScore:      totalScore,
		SkillScore:      skillScore,
		ExperienceScore: expScore,
		LocationScore:   locScore,
		EducationScore:  eduScore,
		MatchDetails:    matchDetails,
	}
}

func (m *CriteriaMatcher) calculateSkillScore(candidate candidateData, criteria SearchCriteria) float64 {
	// Combine skills from DB and CV
	allSkills := strings.ToLower(candidate.Skills + " " + candidate.CVText)

	requiredMatches := 0
	for _, skill := range criteria.RequiredSkills {
		if strings.Contains(allSkills, strings.ToLower(skill)) {
			requiredMatches++
		}
	}

	preferredMatches := 0
	for _, skill := range criteria.PreferredSkills {
		if strings.Contains(allSkills, strings.ToLower(skill)) {
			preferredMatches++
		}
	}

	// Required skills: 70% weight, Preferred: 30%
	requiredScore := 0.0
	if len(criteria.RequiredSkills) > 0 {
		requiredScore = float64(requiredMatches) / float64(len(criteria.RequiredSkills))
	}

	preferredScore := 0.0
	if len(criteria.PreferredSkills) > 0 {
		preferredScore = float64(preferredMatches) / float64(len(criteria.PreferredSkills))
	}

	return (requiredScore * 0.7) + (preferredScore * 0.3)
}

func (m *CriteriaMatcher) calculateExperienceScore(candidate candidateData, criteria SearchCriteria) float64 {
	// Extract years of experience from text (simplified)
	expYears := extractExperienceYears(candidate.Experience + " " + candidate.CVText)

	// Check if experience criteria exists (use nil check for pointers)
	if (criteria.MinExperience == nil || *criteria.MinExperience == 0) &&
		(criteria.MaxExperience == nil || *criteria.MaxExperience == 0) {
		return 1.0 // No experience requirement
	}

	minExp := 0
	maxExp := 0
	if criteria.MinExperience != nil {
		minExp = *criteria.MinExperience
	}
	if criteria.MaxExperience != nil {
		maxExp = *criteria.MaxExperience
	}

	if expYears >= minExp && (maxExp == 0 || expYears <= maxExp) {
		return 1.0
	}

	if minExp > 0 && expYears < minExp {
		// Partial score if close
		diff := minExp - expYears
		return math.Max(0, 1.0-(float64(diff)*0.2))
	}

	return 0.5 // Over max experience
}

func (m *CriteriaMatcher) calculateLocationScore(candidate candidateData, criteria SearchCriteria) float64 {
	if len(criteria.Location) == 0 {
		return 1.0
	}

	// Check if candidate location matches any of the criteria locations
	for _, loc := range criteria.Location {
		if strings.EqualFold(candidate.Location, loc) {
			return 1.0
		}

		// Partial match (e.g., "Istanbul" matches "Istanbul, Turkey")
		if strings.Contains(strings.ToLower(candidate.Location), strings.ToLower(loc)) {
			return 0.8
		}
	}

	return 0.0
}

func (m *CriteriaMatcher) calculateEducationScore(candidate candidateData, criteria SearchCriteria) float64 {
	if len(criteria.Education) == 0 {
		return 1.0
	}

	// Check if candidate has any of the required education
	candidateText := strings.ToLower(candidate.Experience + " " + candidate.CVText)

	for _, edu := range criteria.Education {
		if strings.Contains(candidateText, strings.ToLower(edu)) {
			return 1.0
		}
	}

	return 0.0
}

func (m *CriteriaMatcher) getMatchedSkills(candidate candidateData, skills []string) []string {
	allSkills := strings.ToLower(candidate.Skills + " " + candidate.CVText)
	matched := []string{}

	for _, skill := range skills {
		if strings.Contains(allSkills, strings.ToLower(skill)) {
			matched = append(matched, skill)
		}
	}

	return matched
}

func (m *CriteriaMatcher) saveCandidateScore(ctx context.Context, queryID string, criteria SearchCriteria, score ScoredCandidate) error {
	criteriaJSON, _ := json.Marshal(criteria)
	detailsJSON, _ := json.Marshal(score.MatchDetails)

	_, err := m.db.ExecContext(ctx, `
		INSERT INTO candidate_scores 
		(candidate_id, query_id, criteria, total_score, skill_score, experience_score, location_score, education_score, match_details, ranked_position)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, score.CandidateID, queryID, criteriaJSON, score.TotalScore, score.SkillScore, score.ExperienceScore, score.LocationScore, score.EducationScore, detailsJSON, score.Rank)

	return err
}

// Helper functions
func extractExperienceYears(text string) int {
	// Simplified experience extraction
	// TODO: Use LLM for accurate extraction
	// Look for patterns like "5 years", "5+ years", etc.
	return 0 // Placeholder
}

func sortByScore(candidates []ScoredCandidate) {
	// Bubble sort for simplicity (use sort.Slice in production)
	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			if candidates[j].TotalScore > candidates[i].TotalScore {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}
}
