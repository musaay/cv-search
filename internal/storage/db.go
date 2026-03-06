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

// ─── Candidate Management ────────────────────────────────────────────────────

// UpsertCandidateForGraphNode creates or links a candidate record for a graph person node.
// If a candidate with the same graph_node_id already exists it's a no-op.
// If a candidate with the same name exists but no graph_node_id, we link it.
// Returns the candidate row ID.
func (db *DB) UpsertCandidateForGraphNode(ctx context.Context, graphNodeID int, name string) (int, error) {
	var candidateID int

	// Check if already linked
	err := db.connection.QueryRowContext(ctx,
		`SELECT id FROM candidates WHERE graph_node_id = $1`, graphNodeID,
	).Scan(&candidateID)
	if err == nil {
		return candidateID, nil // already exists
	}
	if err != sql.ErrNoRows {
		return 0, fmt.Errorf("upsert check failed: %w", err)
	}

	// Insert new candidate row
	query := `
		INSERT INTO candidates (name, graph_node_id, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT DO NOTHING
		RETURNING id
	`
	err = db.connection.QueryRowContext(ctx, query, name, graphNodeID).Scan(&candidateID)
	if err != nil {
		// Might have been inserted by a concurrent request; try fetching again
		err2 := db.connection.QueryRowContext(ctx,
			`SELECT id FROM candidates WHERE graph_node_id = $1`, graphNodeID,
		).Scan(&candidateID)
		if err2 != nil {
			return 0, fmt.Errorf("upsert candidate failed: %w", err)
		}
	}
	return candidateID, nil
}

// ListCandidates returns a paginated list of candidates with basic enrichment from graph_nodes.
func (db *DB) ListCandidates(ctx context.Context, limit, offset int) ([]CandidateListItem, error) {
	db.connection.ExecContext(ctx, "DEALLOCATE ALL")

	query := `
		SELECT
			c.id,
			c.name,
			COALESCE(gn.properties->>'current_position', '') AS current_position,
			COALESCE(gn.properties->>'seniority', '')         AS seniority,
			COUNT(i.id)                                        AS interview_count,
			COALESCE(
				(SELECT outcome FROM interviews
				 WHERE candidate_id = c.id
				 ORDER BY interview_date DESC, id DESC
				 LIMIT 1),
				''
			)                                                  AS latest_outcome,
			c.created_at
		FROM candidates c
		LEFT JOIN graph_nodes gn ON gn.id = c.graph_node_id
		LEFT JOIN interviews i   ON i.candidate_id = c.id
		GROUP BY c.id, gn.properties
		ORDER BY c.created_at DESC
		LIMIT $1 OFFSET $2
	`
	rows, err := db.connection.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list candidates failed: %w", err)
	}
	defer rows.Close()

	var result []CandidateListItem
	for rows.Next() {
		var item CandidateListItem
		if err := rows.Scan(
			&item.ID, &item.Name, &item.CurrentPosition, &item.Seniority,
			&item.InterviewCount, &item.LatestOutcome, &item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan candidate list row: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// GetCandidateDetail returns the full candidate profile with all interviews.
func (db *DB) GetCandidateDetail(ctx context.Context, candidateID int) (*CandidateDetail, error) {
	db.connection.ExecContext(ctx, "DEALLOCATE ALL")

	var c CandidateDetail
	var graphNodeID sql.NullInt64
	var email, phone, location sql.NullString

	err := db.connection.QueryRowContext(ctx, `
		SELECT
			c.id, c.name, c.email, c.phone, c.location, c.graph_node_id,
			COALESCE(gn.properties->>'current_position', '') AS current_position,
			COALESCE(gn.properties->>'seniority', '')         AS seniority,
			c.created_at
		FROM candidates c
		LEFT JOIN graph_nodes gn ON gn.id = c.graph_node_id
		WHERE c.id = $1
	`, candidateID).Scan(
		&c.ID, &c.Name, &email, &phone, &location, &graphNodeID,
		&c.CurrentPosition, &c.Seniority, &c.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get candidate detail: %w", err)
	}

	if email.Valid {
		c.Email = email.String
	}
	if phone.Valid {
		c.Phone = phone.String
	}
	if location.Valid {
		c.Location = location.String
	}
	if graphNodeID.Valid {
		id := int(graphNodeID.Int64)
		c.GraphNodeID = &id
	}

	// Load interviews
	interviews, err := db.getInterviewsByCandidate(ctx, candidateID)
	if err != nil {
		return nil, err
	}
	c.Interviews = interviews

	return &c, nil
}

func (db *DB) getInterviewsByCandidate(ctx context.Context, candidateID int) ([]Interview, error) {
	rows, err := db.connection.QueryContext(ctx, `
		SELECT id, candidate_id, interview_date, COALESCE(team,''), COALESCE(interviewer_name,''),
		       COALESCE(interview_type,''), COALESCE(notes,''), COALESCE(outcome,''),
		       created_at, updated_at
		FROM interviews
		WHERE candidate_id = $1
		ORDER BY interview_date DESC, id DESC
	`, candidateID)
	if err != nil {
		return nil, fmt.Errorf("get interviews: %w", err)
	}
	defer rows.Close()

	var result []Interview
	for rows.Next() {
		var iv Interview
		if err := rows.Scan(
			&iv.ID, &iv.CandidateID, &iv.InterviewDate, &iv.Team, &iv.InterviewerName,
			&iv.InterviewType, &iv.Notes, &iv.Outcome, &iv.CreatedAt, &iv.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan interview: %w", err)
		}
		result = append(result, iv)
	}
	return result, rows.Err()
}

// GetInterviewsByGraphNodeIDs batch-fetches interview summaries for a list of graph node IDs.
// Returns a map of graphNodeID → []InterviewSummary for use in search result enrichment.
func (db *DB) GetInterviewsByGraphNodeIDs(ctx context.Context, graphNodeIDs []int) (map[int][]InterviewSummary, error) {
	if len(graphNodeIDs) == 0 {
		return map[int][]InterviewSummary{}, nil
	}

	placeholders := make([]string, len(graphNodeIDs))
	args := make([]interface{}, len(graphNodeIDs))
	for i, id := range graphNodeIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	inClause := "(" + strings.Join(placeholders, ",") + ")"

	query := fmt.Sprintf(`
		SELECT c.graph_node_id, i.id, i.interview_date,
		       COALESCE(i.team,''), COALESCE(i.interviewer_name,''),
		       COALESCE(i.interview_type,''), COALESCE(i.outcome,'')
		FROM interviews i
		JOIN candidates c ON c.id = i.candidate_id
		WHERE c.graph_node_id IN %s
		ORDER BY i.interview_date DESC
	`, inClause)

	rows, err := db.connection.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("batch get interviews: %w", err)
	}
	defer rows.Close()

	result := make(map[int][]InterviewSummary)
	for rows.Next() {
		var nodeID int
		var s InterviewSummary
		if err := rows.Scan(
			&nodeID, &s.ID, &s.InterviewDate, &s.Team, &s.InterviewerName, &s.InterviewType, &s.Outcome,
		); err != nil {
			return nil, fmt.Errorf("scan interview summary: %w", err)
		}
		result[nodeID] = append(result[nodeID], s)
	}
	return result, rows.Err()
}

// GetInterviewNotesByGraphNodeID returns all interview notes concatenated for re-embedding.
func (db *DB) GetInterviewNotesByGraphNodeID(ctx context.Context, graphNodeID int) ([]string, error) {
	rows, err := db.connection.QueryContext(ctx, `
		SELECT COALESCE(i.notes, '')
		FROM interviews i
		JOIN candidates c ON c.id = i.candidate_id
		WHERE c.graph_node_id = $1 AND i.notes <> ''
		ORDER BY i.interview_date DESC
	`, graphNodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notes []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		notes = append(notes, n)
	}
	return notes, rows.Err()
}

// ─── Interview CRUD ───────────────────────────────────────────────────────────

// CreateInterview inserts a new interview record and returns the new ID.
// Returns an error if the candidate does not exist.
func (db *DB) CreateInterview(ctx context.Context, candidateID int, iv Interview) (int, error) {
	var newID int
	err := db.connection.QueryRowContext(ctx, `
		INSERT INTO interviews (candidate_id, interview_date, team, interviewer_name, interview_type, notes, outcome, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW())
		RETURNING id
	`, candidateID, iv.InterviewDate, iv.Team, iv.InterviewerName, iv.InterviewType, iv.Notes, iv.Outcome,
	).Scan(&newID)
	if err != nil {
		return 0, fmt.Errorf("create interview: %w", err)
	}
	return newID, nil
}

// UpdateInterview updates an existing interview. Returns sql.ErrNoRows if not found
// or if the interview doesn't belong to the given candidateID.
func (db *DB) UpdateInterview(ctx context.Context, interviewID, candidateID int, iv Interview) error {
	res, err := db.connection.ExecContext(ctx, `
		UPDATE interviews
		SET interview_date = $1, team = $2, interviewer_name = $3,
		    interview_type = $4, notes = $5, outcome = $6, updated_at = NOW()
		WHERE id = $7 AND candidate_id = $8
	`, iv.InterviewDate, iv.Team, iv.InterviewerName, iv.InterviewType, iv.Notes, iv.Outcome,
		interviewID, candidateID,
	)
	if err != nil {
		return fmt.Errorf("update interview: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteInterview removes a specific interview. Enforces candidateID ownership.
func (db *DB) DeleteInterview(ctx context.Context, interviewID, candidateID int) error {
	res, err := db.connection.ExecContext(ctx, `
		DELETE FROM interviews WHERE id = $1 AND candidate_id = $2
	`, interviewID, candidateID)
	if err != nil {
		return fmt.Errorf("delete interview: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetGraphNodeIDForCandidate returns the graph_node_id for a given candidate.
// Returns 0 if the candidate has no linked graph node.
func (db *DB) GetGraphNodeIDForCandidate(ctx context.Context, candidateID int) (int, error) {
	var nodeID sql.NullInt64
	err := db.connection.QueryRowContext(ctx,
		`SELECT graph_node_id FROM candidates WHERE id = $1`, candidateID,
	).Scan(&nodeID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !nodeID.Valid {
		return 0, nil
	}
	return int(nodeID.Int64), nil
}

// UpdateCVFileCandidateID sets candidate_id on a cv_files row.
func (db *DB) UpdateCVFileCandidateID(ctx context.Context, cvFileID int64, candidateID int) error {
	_, err := db.connection.ExecContext(ctx,
		`UPDATE cv_files SET candidate_id = $1 WHERE id = $2`, candidateID, cvFileID,
	)
	return err
}

// GetPersonGraphNodeIDByName returns the graph_nodes.id for a person with the given name.
// Returns 0 if not found.
func (db *DB) GetPersonGraphNodeIDByName(ctx context.Context, name string) (int, error) {
	var id int
	err := db.connection.QueryRowContext(ctx, `
		SELECT id FROM graph_nodes
		WHERE node_type = 'person' AND properties->>'name' = $1
		ORDER BY created_at DESC
		LIMIT 1
	`, name).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

// SyncCandidateTextFields updates the candidates row (experience, skills, location) from
// the linked graph_nodes data so the tsvector search_vector stays accurate for BM25.
// Should be called after graph building and after any interview that may update the role.
func (db *DB) SyncCandidateTextFields(ctx context.Context, candidateID, graphNodeID int) error {
	// Derive experience text from node properties (position + seniority)
	// Derive skills as comma-joined skill names from HAS_SKILL edges
	_, err := db.connection.ExecContext(ctx, `
		UPDATE candidates c
		SET
		    experience = COALESCE(gn.properties->>'current_position','') ||
		                 CASE WHEN COALESCE(gn.properties->>'seniority','') <> ''
		                      THEN ' ' || (gn.properties->>'seniority')
		                      ELSE '' END,
		    skills = (
		        SELECT string_agg(s.properties->>'name', ',')
		        FROM graph_edges e
		        JOIN graph_nodes s ON e.target_node_id = s.id
		        WHERE e.source_node_id = gn.id
		          AND e.edge_type = 'HAS_SKILL'
		          AND s.node_type = 'skill'
		    )
		FROM graph_nodes gn
		WHERE gn.id = $1
		  AND c.id = $2
	`, graphNodeID, candidateID)
	return err
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
