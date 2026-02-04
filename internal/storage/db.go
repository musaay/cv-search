package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	_ "github.com/lib/pq" // PostgreSQL driver
)

type DB struct {
	connection *sql.DB
}

func NewDB(dataSourceName string) (*DB, error) {
	// Note: We use DEALLOCATE ALL in query methods to prevent prepared statement cache collisions
	// instead of prefer_simple_protocol which is not supported by lib/pq driver

	db, err := sql.Open("postgres", dataSourceName)
	if err != nil {
		return nil, err
	}

	// Connection pool tuning
	// Reduce pool size to minimize prepared statement cache collisions
	db.SetMaxOpenConns(5)                   // Reduced from 25
	db.SetMaxIdleConns(2)                   // Reduced from 10
	db.SetConnMaxLifetime(1 * time.Minute)  // Reduced from 5 minutes
	db.SetConnMaxIdleTime(30 * time.Second) // Force conn reset

	if err := db.Ping(); err != nil {
		return nil, err
	}

	return &DB{connection: db}, nil
}

func (db *DB) Close() {
	if err := db.connection.Close(); err != nil {
		log.Println("Error closing the database connection:", err)
	}
}

// SaveCandidate is kept for backward compatibility and calls the context-aware variant.
func (db *DB) SaveCandidate(candidate *Candidate) error {
	return db.SaveCandidateContext(context.Background(), candidate)
}

func (db *DB) SaveCandidateContext(ctx context.Context, candidate *Candidate) error {
	query := `INSERT INTO candidates (name, email, experience, skills, location, resume_url, resume_file_path, resume_downloaded_at)
              VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
              ON CONFLICT (email) DO UPDATE
                SET name = EXCLUDED.name,
                    experience = EXCLUDED.experience,
                    skills = EXCLUDED.skills,
                    location = EXCLUDED.location,
                    resume_url = EXCLUDED.resume_url,
                    resume_file_path = EXCLUDED.resume_file_path,
                    resume_downloaded_at = EXCLUDED.resume_downloaded_at`
	skills := strings.Join(candidate.Skills, ",")

	var resumeDownloadedAt interface{}
	if candidate.ResumeFilePath != "" {
		resumeDownloadedAt = time.Now()
	}

	_, err := db.connection.ExecContext(ctx, query,
		candidate.Name,
		candidate.Email,
		candidate.Experience,
		skills,
		candidate.Location,
		candidate.ResumeURL,
		candidate.ResumeFilePath,
		resumeDownloadedAt,
	)
	return err
}

// GetCandidateByEmail is kept for backward compatibility and calls the context-aware variant.
func (db *DB) GetCandidateByEmail(email string) (*Candidate, error) {
	return db.GetCandidateByEmailContext(context.Background(), email)
}

func (db *DB) GetCandidateByEmailContext(ctx context.Context, email string) (*Candidate, error) {
	candidate := &Candidate{}
	query := `SELECT name, email, experience, skills, location FROM candidates WHERE email = $1`
	row := db.connection.QueryRowContext(ctx, query, email)
	var skills string
	err := row.Scan(&candidate.Name, &candidate.Email, &candidate.Experience, &skills, &candidate.Location)
	if err != nil {
		return nil, err
	}
	if skills != "" {
		candidate.Skills = splitAndTrim(skills)
	}
	return candidate, nil
}

// SearchCandidates returns candidates matching the provided criteria using ILIKE and simple skills match.
func (db *DB) SearchCandidates(ctx context.Context, criteria *Criteria) ([]*Candidate, error) {
	base := `SELECT name, email, experience, skills, location FROM candidates`
	var where []string
	var args []interface{}
	i := 1

	if criteria == nil {
		criteria = &Criteria{}
	}

	if criteria.Name != "" {
		where = append(where, fmt.Sprintf("name ILIKE $%d", i))
		args = append(args, "%"+criteria.Name+"%")
		i++
	}
	if criteria.Location != "" {
		where = append(where, fmt.Sprintf("location ILIKE $%d", i))
		args = append(args, "%"+criteria.Location+"%")
		i++
	}
	if len(criteria.Skills) > 0 {
		var skillConds []string
		for _, s := range criteria.Skills {
			skillConds = append(skillConds, fmt.Sprintf("skills ILIKE $%d", i))
			args = append(args, "%"+s+"%")
			i++
		}
		where = append(where, "("+strings.Join(skillConds, " OR ")+")")
	}

	if len(where) > 0 {
		base += " WHERE " + strings.Join(where, " AND ")
	}

	rows, err := db.connection.QueryContext(ctx, base, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []*Candidate
	for rows.Next() {
		c := &Candidate{}
		var skills string
		if err := rows.Scan(&c.Name, &c.Email, &c.Experience, &skills, &c.Location); err != nil {
			return nil, err
		}
		if skills != "" {
			c.Skills = splitAndTrim(skills)
		}
		res = append(res, c)
	}
	return res, rows.Err()
}

// CandidateExists checks if a candidate with the given email already exists
func (db *DB) CandidateExists(ctx context.Context, email string) (bool, error) {
	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM candidates WHERE email = $1)`
	err := db.connection.QueryRowContext(ctx, query, email).Scan(&exists)
	return exists, err
}

// GetCandidateLastUpdated returns the last update timestamp for a candidate
func (db *DB) GetCandidateLastUpdated(ctx context.Context, email string) (time.Time, error) {
	var updatedAt time.Time
	query := `SELECT updated_at FROM candidates WHERE email = $1`
	err := db.connection.QueryRowContext(ctx, query, email).Scan(&updatedAt)
	return updatedAt, err
}

// ShouldUpdateCandidate checks if candidate data should be refreshed based on last update
// Returns true if candidate doesn't exist or if it's been more than updateInterval since last update
func (db *DB) ShouldUpdateCandidate(ctx context.Context, email string, updateInterval time.Duration) (bool, error) {
	exists, err := db.CandidateExists(ctx, email)
	if err != nil {
		return false, err
	}

	if !exists {
		return true, nil // New candidate, should fetch
	}

	lastUpdated, err := db.GetCandidateLastUpdated(ctx, email)
	if err != nil {
		return true, nil // Error getting timestamp, safer to update
	}

	// Check if enough time has passed since last update
	timeSinceUpdate := time.Since(lastUpdated)
	return timeSinceUpdate > updateInterval, nil
}

// helper to split comma-separated skills
func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// SaveCVFileWithHash saves CV file with content hash for duplicate detection
func (db *DB) SaveCVFileWithHash(ctx context.Context, candidateID *int, filename, filePath, fileType, parsedText string, fileSize int64, contentHash string) (int, error) {
	var cvID int
	query := `
        INSERT INTO cv_files (candidate_id, filename, file_path, file_type, file_size, parsed_text, content_hash, uploaded_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
        ON CONFLICT (content_hash) DO UPDATE 
        SET uploaded_at = NOW()
        RETURNING id
    `

	log.Printf("[DB] Saving CV with hash: %s (length: %d)", contentHash[:16], len(contentHash))

	err := db.connection.QueryRowContext(ctx, query,
		candidateID, filename, filePath, fileType, fileSize, parsedText, contentHash,
	).Scan(&cvID)

	if err != nil {
		log.Printf("[DB] Error saving CV with hash: %v", err)
		return 0, err
	}

	log.Printf("[DB] CV saved successfully with ID: %d", cvID)
	return cvID, nil
}

// FindCVByHash checks if a CV with the same content hash already exists
func (db *DB) FindCVByHash(ctx context.Context, contentHash string) (*CVFileInfo, error) {
	var info CVFileInfo
	query := `
        SELECT id, filename, file_size, uploaded_at, candidate_id
        FROM cv_files
        WHERE content_hash = $1
        LIMIT 1
    `
	err := db.connection.QueryRowContext(ctx, query, contentHash).Scan(
		&info.ID, &info.Filename, &info.FileSize, &info.UploadedAt, &info.CandidateID,
	)

	if err == sql.ErrNoRows {
		return nil, nil // Not found
	}
	if err != nil {
		return nil, err
	}

	return &info, nil
}

// SaveCVEntity saves extracted entity from CV
func (db *DB) SaveCVEntity(ctx context.Context, cvFileID int, entityType, entityValue string, confidence float64) error {
	query := `
        INSERT INTO cv_entities (cv_file_id, entity_type, entity_value, confidence)
        VALUES ($1, $2, $3, $4)
    `
	_, err := db.connection.ExecContext(ctx, query, cvFileID, entityType, entityValue, confidence)
	return err
}

// GetConnection returns the underlying database connection for advanced queries
func (db *DB) GetConnection() *sql.DB {
	return db.connection
}

// CreateCVUploadJob creates a new async CV processing job
func (db *DB) CreateCVUploadJob(ctx context.Context, cvFileID int64) (int64, error) {
	query := `
        INSERT INTO cv_upload_jobs (cv_file_id, status, created_at)
        VALUES ($1, 'pending', NOW())
        RETURNING id
    `
	var jobID int64
	err := db.connection.QueryRowContext(ctx, query, cvFileID).Scan(&jobID)
	if err != nil {
		return 0, err
	}

	// Update cv_files with job_id
	_, err = db.connection.ExecContext(ctx, `UPDATE cv_files SET job_id = $1 WHERE id = $2`, jobID, cvFileID)
	if err != nil {
		log.Printf("[DB] Warning: Failed to update cv_files.job_id: %v", err)
	}

	return jobID, nil
}

// UpdateJobStatus updates job status and timestamps
func (db *DB) UpdateJobStatus(ctx context.Context, jobID int64, status string, errorMsg *string) error {
	var query string
	var args []interface{}

	switch status {
	case "processing":
		query = `UPDATE cv_upload_jobs SET status = $1, started_at = NOW() WHERE id = $2`
		args = []interface{}{status, jobID}
	case "completed":
		query = `UPDATE cv_upload_jobs SET status = $1, completed_at = NOW() WHERE id = $2`
		args = []interface{}{status, jobID}
	case "failed":
		query = `UPDATE cv_upload_jobs SET status = $1, error_message = $2, completed_at = NOW() WHERE id = $3`
		args = []interface{}{status, errorMsg, jobID}
	default:
		query = `UPDATE cv_upload_jobs SET status = $1 WHERE id = $2`
		args = []interface{}{status, jobID}
	}

	_, err := db.connection.ExecContext(ctx, query, args...)
	return err
}

// GetJobByID retrieves a job by ID
func (db *DB) GetJobByID(ctx context.Context, jobID int64) (*CVUploadJob, error) {
	// Clear prepared statement cache to prevent binding errors
	db.connection.Exec("DEALLOCATE ALL")

	query := `
        SELECT id, cv_file_id, status, error_message, progress,
               created_at, started_at, completed_at, retry_count, max_retries
        FROM cv_upload_jobs
        WHERE id = $1
    `
	var job CVUploadJob
	var progressJSON sql.NullString

	err := db.connection.QueryRowContext(ctx, query, jobID).Scan(
		&job.ID, &job.CVFileID, &job.Status, &job.ErrorMessage,
		&progressJSON, &job.CreatedAt, &job.StartedAt, &job.CompletedAt,
		&job.RetryCount, &job.MaxRetries,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	job.Progress = make(map[string]interface{})

	return &job, nil
}
