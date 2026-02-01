package cv

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"code.sajari.com/docconv"
)

type CVParser struct {
	uploadsDir string
}

type ParsedCV struct {
	Filename    string
	FileType    string
	FileSize    int64
	FullText    string
	Entities    []Entity
	Skills      []string
	Companies   []string
	Education   []string
	Certificates []string
}

type Entity struct {
	Type       string  // skill, company, education, certification
	Value      string
	Confidence float64
}

func NewCVParser(uploadsDir string) *CVParser {
	return &CVParser{
		uploadsDir: uploadsDir,
	}
}

// ParseFile extracts text from PDF/DOCX/TXT files
func (p *CVParser) ParseFile(filename string, reader io.Reader) (*ParsedCV, error) {
	// Save file temporarily
	filePath := filepath.Join(p.uploadsDir, filename)
	if err := os.MkdirAll(p.uploadsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create uploads dir: %w", err)
	}

	file, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	size, err := io.Copy(file, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to save file: %w", err)
	}

	// Extract text based on file type
	fileType := strings.ToLower(filepath.Ext(filename))
	var text string

	switch fileType {
	case ".pdf", ".docx", ".doc", ".rtf", ".odt":
		// Use docconv for PDF/DOCX parsing
		res, err := docconv.ConvertPath(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to parse document: %w", err)
		}
		text = res.Body
	case ".txt":
		// Plain text
		content, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read text file: %w", err)
		}
		text = string(content)
	default:
		return nil, fmt.Errorf("unsupported file type: %s", fileType)
	}

	return &ParsedCV{
		Filename: filename,
		FileType: fileType,
		FileSize: size,
		FullText: text,
	}, nil
}

// ExtractBasicEntities performs basic entity extraction without LLM
// For production, this should be replaced with LLM-based extraction
func (p *CVParser) ExtractBasicEntities(text string) []Entity {
	entities := []Entity{}

	// Common skill keywords
	skillKeywords := []string{
		"Go", "Golang", "Python", "Java", "JavaScript", "TypeScript",
		"React", "Vue", "Angular", "Node.js", "Docker", "Kubernetes",
		"PostgreSQL", "MySQL", "MongoDB", "Redis", "AWS", "Azure", "GCP",
		"GraphQL", "REST", "API", "Microservices", "Git", "CI/CD",
		"Machine Learning", "AI", "Data Science", "DevOps",
	}

	textLower := strings.ToLower(text)
	for _, skill := range skillKeywords {
		if strings.Contains(textLower, strings.ToLower(skill)) {
			entities = append(entities, Entity{
				Type:       "skill",
				Value:      skill,
				Confidence: 0.8, // Basic keyword matching
			})
		}
	}

	return entities
}
