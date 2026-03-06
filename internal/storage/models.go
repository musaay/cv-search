package storage

import "time"

// Candidate represents a scraped/stored candidate.
// Note: Keep this minimal for DB persistence; enrich elsewhere if needed.
type Candidate struct {
	Name               string   `json:"name"`
	Email              string   `json:"email"`
	Experience         string   `json:"experience"`
	Skills             []string `json:"skills"`
	Location           string   `json:"location"`
	ResumeURL          string   `json:"resume_url,omitempty"`
	ResumeFilePath     string   `json:"resume_file_path,omitempty"`
	ResumeDownloadedAt string   `json:"resume_downloaded_at,omitempty"`
}

// Interview represents a single interview session for a candidate.
// Multiple interviews can exist per candidate (different teams, dates, rounds).
type Interview struct {
	ID              int        `json:"id"`
	CandidateID     int        `json:"candidate_id"`
	InterviewDate   time.Time  `json:"interview_date"`
	Team            string     `json:"team,omitempty"`
	InterviewerName string     `json:"interviewer_name,omitempty"`
	InterviewType   string     `json:"interview_type,omitempty"` // technical, hr, case_study, other
	Notes           string     `json:"notes,omitempty"`
	Outcome         string     `json:"outcome,omitempty"` // passed, failed, pending
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// InterviewSummary is a lightweight view of an interview for embedding in search results.
// Does not include raw notes to keep search responses lean.
type InterviewSummary struct {
	ID              int       `json:"id"`
	InterviewDate   time.Time `json:"interview_date"`
	Team            string    `json:"team,omitempty"`
	InterviewerName string    `json:"interviewer_name,omitempty"`
	InterviewType   string    `json:"interview_type,omitempty"`
	Outcome         string    `json:"outcome,omitempty"`
}

// CandidateDetail is a full candidate profile including all interviews.
type CandidateDetail struct {
	ID              int         `json:"id"`
	Name            string      `json:"name"`
	Email           string      `json:"email,omitempty"`
	Phone           string      `json:"phone,omitempty"`
	Location        string      `json:"location,omitempty"`
	GraphNodeID     *int        `json:"graph_node_id,omitempty"`
	CurrentPosition string      `json:"current_position,omitempty"` // from graph_nodes.properties
	Seniority       string      `json:"seniority,omitempty"`        // from graph_nodes.properties
	Interviews      []Interview `json:"interviews"`
	CreatedAt       time.Time   `json:"created_at"`
}

// CandidateListItem is a lightweight row for the candidate list endpoint.
type CandidateListItem struct {
	ID              int       `json:"id"`
	Name            string    `json:"name"`
	CurrentPosition string    `json:"current_position,omitempty"` // from graph_nodes.properties
	Seniority       string    `json:"seniority,omitempty"`
	InterviewCount  int       `json:"interview_count"`
	LatestOutcome   string    `json:"latest_outcome,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
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
