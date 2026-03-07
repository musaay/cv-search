package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

type Provider string

const (
	ProviderOpenAI Provider = "openai"
	ProviderOllama Provider = "ollama"
	ProviderGroq   Provider = "groq"
	ProviderNone   Provider = "none"
)

type Service struct {
	provider Provider
	apiKey   string
	model    string
	timeout  time.Duration
}

type CVExtraction struct {
	Candidate Candidate   `json:"candidate"`
	Skills    []Skill     `json:"skills"`
	Companies []Company   `json:"companies"`
	Education []Education `json:"education"`
	Locations []string    `json:"locations"`
	Languages []string    `json:"languages"`
}

type Candidate struct {
	Name                 string      `json:"name"`
	CurrentPosition      string      `json:"current_position"`
	Seniority            string      `json:"seniority"`
	TotalExperienceYears interface{} `json:"total_experience_years"` // Can be int, string, or null
}

type Skill struct {
	Name           string  `json:"skill"`
	Proficiency    string  `json:"proficiency"`
	Years          *int    `json:"years"`
	Confidence     float64 `json:"confidence"`
	NormalizedFrom string  `json:"normalized_from,omitempty"`
}

type Company struct {
	Name          string      `json:"name"`
	Position      string      `json:"position"`
	DurationYears interface{} `json:"duration_years"` // Can be int or float
	StartYear     interface{} `json:"start_year"`     // Can be int or string
	EndYear       interface{} `json:"end_year"`       // Can be int or string
	IsCurrent     bool        `json:"is_current"`
	Confidence    float64     `json:"confidence"`
}

type Education struct {
	Degree         string      `json:"degree"`
	Field          string      `json:"field"`
	Institution    string      `json:"institution"`
	GraduationYear interface{} `json:"graduation_year"` // Can be int or string
}

func NewService(provider, apiKey, model string) *Service {
	return &Service{
		provider: Provider(provider),
		apiKey:   apiKey,
		model:    model,
		timeout:  600 * time.Second, // 10 minutes for large CVs and slower models
	}
}

// Generate sends a prompt to LLM and returns the response (for GraphRAG queries)
func (s *Service) Generate(prompt string) (string, error) {
	if s.provider == ProviderNone {
		return "", fmt.Errorf("LLM provider not configured")
	}

	var response string
	var err error

	switch s.provider {
	case ProviderOpenAI:
		response, err = s.callOpenAI(prompt)
	case ProviderOllama:
		response, err = s.callOllama(prompt)
	case ProviderGroq:
		response, err = s.callGroq(prompt)
	default:
		return "", fmt.Errorf("unknown provider: %s", s.provider)
	}

	return response, err
}

func (s *Service) ExtractEntities(cvText string) (*CVExtraction, error) {
	if s.provider == ProviderNone {
		return nil, fmt.Errorf("LLM provider not configured")
	}

	prompt := s.buildPrompt(cvText)

	var response string
	var err error

	switch s.provider {
	case ProviderOpenAI:
		response, err = s.callOpenAI(prompt)
	case ProviderOllama:
		response, err = s.callOllama(prompt)
	case ProviderGroq:
		response, err = s.callGroq(prompt)
	default:
		return nil, fmt.Errorf("unknown provider: %s", s.provider)
	}

	if err != nil {
		return nil, err
	}

	var extraction CVExtraction
	err = json.Unmarshal([]byte(response), &extraction)
	if err != nil {
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	return &extraction, nil
}

func (s *Service) buildPrompt(cvText string) string {
	return fmt.Sprintf(`You are an expert CV parser. Extract structured information from this CV.

CV Text:
"""
%s
"""

Extract and return ONLY valid JSON (no markdown, no explanation) with this exact structure:
{
  "candidate": {
    "name": "Full name",
    "current_position": "Current job title",
    "seniority": "Junior|Mid-level|Senior|Lead|Architect",
    "total_experience_years": 0
  },
  "skills": [
    {
      "skill": "Canonical skill name",
      "proficiency": "Beginner|Intermediate|Advanced|Expert",
      "years": null,
      "confidence": 0.95,
      "normalized_from": "Original text if normalized"
    }
  ],
  "companies": [
    {
      "name": "Company name",
      "position": "Job title",
      "duration_years": null,
      "start_year": null,
      "end_year": null,
      "is_current": false,
      "confidence": 0.95
    }
  ],
  "education": [
    {
      "degree": "Degree type",
      "field": "Field of study",
      "institution": "University name",
      "graduation_year": null
    }
  ],
  "locations": ["City names"],
  "languages": ["Language names"]
}

Important:
- Normalize skill names (e.g., "K8s" → "Kubernetes", "JS" → "JavaScript", "React.js" → "React")
- Infer proficiency from context (e.g., "expert in Java" → "Expert", "familiar with Python" → "Beginner")
- For skills, calculate years from work history (e.g., "Java at Company X (2018-2023)" → years: 5)
- If skill mentioned multiple times, sum all usage periods
- Calculate duration from date ranges if available
- Extract implicit skills (e.g., "built microservices" → add "Microservices")
- Return empty arrays if no data found for a category
- Use null for missing numeric values
- For Turkish text, extract in English`, cvText)
}

func (s *Service) callOpenAI(prompt string) (string, error) {
	reqBody := map[string]interface{}{
		"model": s.model,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You are a CV parser. Return only valid JSON.",
			},
			{
				"role":    "user",
				"content": prompt,
			},
		},
		"temperature": 0.0, // Deterministic: no hallucination in CV parsing/reranking
		"response_format": map[string]string{
			"type": "json_object",
		},
	}

	jsonData, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST",
		"https://api.openai.com/v1/chat/completions",
		bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: s.timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenAI API error: %d", resp.StatusCode)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return "", err
	}

	if result.Error.Message != "" {
		return "", fmt.Errorf("OpenAI error: %s", result.Error.Message)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no response from OpenAI")
	}

	return result.Choices[0].Message.Content, nil
}

func (s *Service) callOllama(prompt string) (string, error) {
	log.Printf("[DEBUG] Calling Ollama with model: %s", s.model)
	log.Printf("[DEBUG] Prompt length: %d characters", len(prompt))
	log.Printf("[DEBUG] Timeout: %v", s.timeout)

	reqBody := map[string]interface{}{
		"model":  s.model,
		"prompt": prompt,
		"stream": false,
		"format": "json",
		"options": map[string]interface{}{
			"temperature": 0.0, // Deterministic: no hallucination in CV parsing/reranking
		},
	}

	jsonData, _ := json.Marshal(reqBody)
	log.Printf("[DEBUG] Request body size: %d bytes", len(jsonData))

	req, err := http.NewRequest("POST",
		"http://localhost:11434/api/generate",
		bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")

	log.Printf("[DEBUG] Sending request to Ollama...")
	startTime := time.Now()

	client := &http.Client{Timeout: s.timeout}
	resp, err := client.Do(req)

	elapsed := time.Since(startTime)
	log.Printf("[DEBUG] Ollama request took: %v", elapsed)

	if err != nil {
		log.Printf("[ERROR] Ollama request failed after %v: %v", elapsed, err)
		return "", fmt.Errorf("Ollama connection failed (is Ollama running?): %w", err)
	}
	defer resp.Body.Close()

	log.Printf("[DEBUG] Ollama response status: %d", resp.StatusCode)

	var result struct {
		Response string `json:"response"`
		Error    string `json:"error"`
	}

	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		log.Printf("[ERROR] Failed to decode Ollama response: %v", err)
		return "", err
	}

	if result.Error != "" {
		log.Printf("[ERROR] Ollama returned error: %s", result.Error)
		return "", fmt.Errorf("Ollama error: %s", result.Error)
	}

	log.Printf("[DEBUG] Ollama response length: %d characters", len(result.Response))
	log.Printf("[DEBUG] Ollama response preview: %.200s...", result.Response)

	return result.Response, nil
}

func (s *Service) callGroq(prompt string) (string, error) {
	const maxRetries = 3

	reqBody := map[string]interface{}{
		"model": s.model,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You are a CV parser. Return only valid JSON.",
			},
			{
				"role":    "user",
				"content": prompt,
			},
		},
		"temperature": 0.0,
		"response_format": map[string]string{
			"type": "json_object",
		},
	}

	jsonData, _ := json.Marshal(reqBody)
	client := &http.Client{Timeout: s.timeout}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			log.Printf("[Groq] Retry attempt %d/%d", attempt, maxRetries)
		}

		req, err := http.NewRequest("POST",
			"https://api.groq.com/openai/v1/chat/completions",
			bytes.NewBuffer(jsonData))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
		req.Header.Set("Content-Type", "application/json")

		startTime := time.Now()
		resp, err := client.Do(req)
		elapsed := time.Since(startTime)
		log.Printf("[Groq] Request took: %v (attempt %d)", elapsed, attempt+1)

		if err != nil {
			if attempt == maxRetries {
				return "", fmt.Errorf("Groq API error after %d attempts: %w", maxRetries+1, err)
			}
			waitDur := time.Duration(1<<attempt) * time.Second
			log.Printf("[Groq] Network error, retrying in %v: %v", waitDur, err)
			time.Sleep(waitDur)
			continue
		}

		// Handle rate limiting with retry
		if resp.StatusCode == http.StatusTooManyRequests {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if attempt == maxRetries {
				return "", fmt.Errorf("Groq rate limited (429) after %d attempts: %s", maxRetries+1, string(body))
			}

			// Respect Retry-After header if present, else exponential backoff
			waitDur := time.Duration(1<<attempt) * time.Second
			if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
				if secs, parseErr := strconv.Atoi(retryAfter); parseErr == nil && secs > 0 {
					waitDur = time.Duration(secs) * time.Second
				}
			}
			log.Printf("[Groq] Rate limited (429), waiting %v before retry...", waitDur)
			time.Sleep(waitDur)
			continue
		}

		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			if attempt == maxRetries {
				return "", fmt.Errorf("Groq API error %d: %s", resp.StatusCode, string(body))
			}
			waitDur := time.Duration(1<<attempt) * time.Second
			log.Printf("[Groq] HTTP %d, retrying in %v", resp.StatusCode, waitDur)
			time.Sleep(waitDur)
			continue
		}

		var result struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return "", err
		}
		if result.Error.Message != "" {
			return "", fmt.Errorf("Groq error: %s", result.Error.Message)
		}
		if len(result.Choices) == 0 {
			return "", fmt.Errorf("no response from Groq")
		}

		log.Printf("[Groq] Response length: %d chars", len(result.Choices[0].Message.Content))
		return result.Choices[0].Message.Content, nil
	}

	return "", fmt.Errorf("Groq: exhausted %d retries", maxRetries+1)
}
