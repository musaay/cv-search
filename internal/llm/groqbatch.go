package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// This file implements Groq's Batch API (https://console.groq.com/docs/batch):
// submit a JSONL file of chat-completion requests, poll for completion, and
// fetch results once ready. Batch requests do NOT count against the standard
// per-model rate limit (a separate quota), and are billed at a 50% discount —
// ideal for bulk CV extraction (initial large imports or backlog reprocessing)
// where near-instant results aren't required.
//
// Only available for the Groq provider; callers should check the provider
// before using these methods.

const groqAPIBase = "https://api.groq.com/openai/v1"

// GroqBatchStatus mirrors the subset of Groq's Batch object we care about.
type GroqBatchStatus struct {
	ID            string `json:"id"`
	Status        string `json:"status"` // validating, failed, in_progress, finalizing, completed, expired, cancelling, cancelled
	InputFileID   string `json:"input_file_id"`
	OutputFileID  string `json:"output_file_id"`
	ErrorFileID   string `json:"error_file_id"`
	CompletionWnd string `json:"completion_window"`
	RequestCounts struct {
		Total     int `json:"total"`
		Completed int `json:"completed"`
		Failed    int `json:"failed"`
	} `json:"request_counts"`
}

type batchRequestLine struct {
	CustomID string          `json:"custom_id"`
	Method   string          `json:"method"`
	URL      string          `json:"url"`
	Body     json.RawMessage `json:"body"`
}

type batchResultLine struct {
	CustomID string `json:"custom_id"`
	Response *struct {
		StatusCode int             `json:"status_code"`
		Body       json.RawMessage `json:"body"`
	} `json:"response"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// SubmitExtractionBatch builds a JSONL batch of CV entity-extraction requests
// (one per item, keyed by custom_id — callers should pass the cv_file_id as a
// string) and submits it to Groq's Batch API. Returns the Groq batch ID and
// the uploaded input file ID for record-keeping.
func (s *Service) SubmitExtractionBatch(items map[string]string, completionWindow string) (groqBatchID, inputFileID string, err error) {
	if s.provider != ProviderGroq {
		return "", "", fmt.Errorf("batch API only supported for Groq provider, got %q", s.provider)
	}
	if len(items) == 0 {
		return "", "", fmt.Errorf("no items to submit")
	}
	if completionWindow == "" {
		completionWindow = "24h"
	}

	var buf bytes.Buffer
	for customID, cvText := range items {
		reqBody := map[string]interface{}{
			"model": s.model,
			"messages": []map[string]string{
				{"role": "system", "content": "You are a CV parser. Return only valid JSON."},
				{"role": "user", "content": s.buildPrompt(cvText)},
			},
			"temperature":     0.0,
			"response_format": map[string]string{"type": "json_object"},
		}
		bodyJSON, marshalErr := json.Marshal(reqBody)
		if marshalErr != nil {
			return "", "", fmt.Errorf("failed to marshal request for %s: %w", customID, marshalErr)
		}
		line := batchRequestLine{
			CustomID: customID,
			Method:   "POST",
			URL:      "/v1/chat/completions",
			Body:     bodyJSON,
		}
		lineJSON, marshalErr := json.Marshal(line)
		if marshalErr != nil {
			return "", "", fmt.Errorf("failed to marshal batch line for %s: %w", customID, marshalErr)
		}
		buf.Write(lineJSON)
		buf.WriteByte('\n')
	}

	inputFileID, err = s.uploadBatchFile(buf.Bytes())
	if err != nil {
		return "", "", fmt.Errorf("failed to upload batch file: %w", err)
	}

	groqBatchID, err = s.createBatch(inputFileID, completionWindow)
	if err != nil {
		return "", inputFileID, fmt.Errorf("failed to create batch job: %w", err)
	}

	return groqBatchID, inputFileID, nil
}

func (s *Service) uploadBatchFile(jsonlData []byte) (fileID string, err error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if err := writer.WriteField("purpose", "batch"); err != nil {
		return "", err
	}
	part, err := writer.CreateFormFile("file", "batch_input.jsonl")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(jsonlData); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", groqAPIBase+"/files", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Groq files upload error %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("failed to parse files response: %w", err)
	}
	return result.ID, nil
}

func (s *Service) createBatch(inputFileID, completionWindow string) (batchID string, err error) {
	reqBody := map[string]string{
		"completion_window": completionWindow,
		"endpoint":          "/v1/chat/completions",
		"input_file_id":     inputFileID,
	}
	jsonData, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", groqAPIBase+"/batches", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Groq create batch error %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("failed to parse batch creation response: %w", err)
	}
	return result.ID, nil
}

// GetGroqBatchStatus fetches the current status of a submitted batch.
func (s *Service) GetGroqBatchStatus(groqBatchID string) (*GroqBatchStatus, error) {
	if s.provider != ProviderGroq {
		return nil, fmt.Errorf("batch API only supported for Groq provider")
	}

	req, err := http.NewRequest("GET", groqAPIBase+"/batches/"+groqBatchID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Groq get batch error %d: %s", resp.StatusCode, string(respBody))
	}

	var status GroqBatchStatus
	if err := json.Unmarshal(respBody, &status); err != nil {
		return nil, fmt.Errorf("failed to parse batch status: %w", err)
	}
	return &status, nil
}

// FetchExtractionBatchResults downloads and parses a completed batch's output
// file, returning a map of custom_id -> extracted CV data, plus a map of
// custom_id -> error message for any lines that failed within the batch.
func (s *Service) FetchExtractionBatchResults(outputFileID string) (map[string]*CVExtraction, map[string]string, error) {
	if outputFileID == "" {
		return nil, nil, fmt.Errorf("empty output file id")
	}

	req, err := http.NewRequest("GET", groqAPIBase+"/files/"+outputFileID+"/content", nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("Groq fetch batch results error %d: %s", resp.StatusCode, string(body))
	}

	results := make(map[string]*CVExtraction)
	errorsByID := make(map[string]string)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // allow long lines (large CV completions)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var result batchResultLine
		if err := json.Unmarshal(line, &result); err != nil {
			continue
		}

		if result.Error != nil {
			errorsByID[result.CustomID] = result.Error.Message
			continue
		}
		if result.Response == nil || result.Response.StatusCode != http.StatusOK {
			errorsByID[result.CustomID] = fmt.Sprintf("batch line returned status %d", result.Response.StatusCode)
			continue
		}

		var chatResp struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(result.Response.Body, &chatResp); err != nil {
			errorsByID[result.CustomID] = fmt.Sprintf("failed to parse chat completion body: %v", err)
			continue
		}
		if len(chatResp.Choices) == 0 {
			errorsByID[result.CustomID] = "no choices in chat completion"
			continue
		}

		var extraction CVExtraction
		if err := json.Unmarshal([]byte(chatResp.Choices[0].Message.Content), &extraction); err != nil {
			errorsByID[result.CustomID] = fmt.Sprintf("failed to parse extraction JSON: %v", err)
			continue
		}
		results[result.CustomID] = &extraction
	}
	if err := scanner.Err(); err != nil {
		return results, errorsByID, fmt.Errorf("error reading batch results: %w", err)
	}

	return results, errorsByID, nil
}
