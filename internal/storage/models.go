package storage

import "time"

// Candidate represents a scraped/stored candidate.
// Note: Keep this minimal for DB persistence; enrich elsewhere if needed.
type Candidate struct {
    Name              string   `json:"name"`
    Email             string   `json:"email"`
    Experience        string   `json:"experience"`
    Skills            []string `json:"skills"`
    Location          string   `json:"location"`
    ResumeURL         string   `json:"resume_url,omitempty"`
    ResumeFilePath    string   `json:"resume_file_path,omitempty"`
    ResumeDownloadedAt string   `json:"resume_downloaded_at,omitempty"`
}

// Criteria used to search for candidates.
type Criteria struct {
    Name     string   `json:"name"`
    Location string   `json:"location"`
    Skills   []string `json:"skills"`
}

// CVFileInfo represents metadata about an uploaded CV file
type CVFileInfo struct {
    ID          int64
    Filename    string
    FileSize    int64
    UploadedAt  time.Time
    CandidateID *int
}

// PersonInfo represents a person node in the knowledge graph
type PersonInfo struct {
    NodeID          string
    Name            string
    CurrentPosition string
}

// CVUploadJob represents an async CV processing job
type CVUploadJob struct {
    ID           int64
    CVFileID     int64
    Status       string // pending, processing, completed, failed
    ErrorMessage *string
    Progress     map[string]interface{}
    CreatedAt    time.Time
    StartedAt    *time.Time
    CompletedAt  *time.Time
    RetryCount   int
    MaxRetries   int
}