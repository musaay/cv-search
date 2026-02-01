package storage

import (
    "context"
    "database/sql"
    "log"
    "fmt"
    "strings"
    "time"

    _ "github.com/lib/pq" // PostgreSQL driver
)

type DB struct {
    connection *sql.DB
}

func NewDB(dataSourceName string) (*DB, error) {
    db, err := sql.Open("postgres", dataSourceName)
    if err != nil {
        return nil, err
    }

    // Connection pool tuning
    db.SetMaxOpenConns(25)
    db.SetMaxIdleConns(10)
    db.SetConnMaxLifetime(5 * time.Minute)

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
// SaveCVFile saves CV file metadata and parsed text to database
func (db *DB) SaveCVFile(ctx context.Context, candidateID *int, filename, filePath, fileType, parsedText string, fileSize int64) (int, error) {
    var cvID int
    query := `
        INSERT INTO cv_files (candidate_id, filename, file_path, file_type, file_size, parsed_text, uploaded_at)
        VALUES ($1, $2, $3, $4, $5, $6, NOW())
        RETURNING id
    `
    err := db.connection.QueryRowContext(ctx, query, 
        candidateID, filename, filePath, fileType, fileSize, parsedText,
    ).Scan(&cvID)
    
    return cvID, err
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
