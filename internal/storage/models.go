package storage

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