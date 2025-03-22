package internal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"
)

// Direction represents the conversion direction
type Direction string

// Format represents the front matter format
type Format string

// Conversion constants
const (
	DirectionHexoToHugo Direction = "hexo2hugo"
	DirectionHugoToHexo Direction = "hugo2hexo"

	FormatYAML Format = "yaml"
	FormatTOML Format = "toml"

	DefaultMaxConcurrency = 4
	DefaultFileExtension  = ".md"
	FrontMatterDelimiter  = "---"
)

// Config defines the settings for the conversion process
type Config struct {
	SourceFormat        Format
	TargetFormat        Format
	FileExtension       string
	MaxConcurrency      int
	ConversionDirection Direction
}

// NewDefaultConfig returns a configuration with default settings
func NewDefaultConfig() *Config {
	return &Config{
		SourceFormat:        FormatYAML,
		TargetFormat:        FormatYAML,
		FileExtension:       DefaultFileExtension,
		MaxConcurrency:      DefaultMaxConcurrency,
		ConversionDirection: DirectionHexoToHugo,
	}
}

// Error definitions
var (
	ErrInvalidMarkdown   = errors.New("invalid markdown: missing front matter delimiters")
	ErrUnsupportedFormat = errors.New("unsupported format")
)

// ConversionError represents an error during the conversion process
type ConversionError struct {
	SourceFile string
	Err        error
}

func (e *ConversionError) Error() string {
	return fmt.Sprintf("converting file %s: %v", e.SourceFile, e.Err)
}

func (e *ConversionError) Unwrap() error {
	return e.Err
}

// FormatHandler defines an interface for front matter processing
type FormatHandler interface {
	Unmarshal(data []byte, v interface{}) error
	Marshal(w io.Writer, v interface{}) error
}

// YAMLHandler handles YAML front matter
type YAMLHandler struct{}

func (h YAMLHandler) Unmarshal(data []byte, v interface{}) error {
	return yaml.Unmarshal(data, v)
}

func (h YAMLHandler) Marshal(w io.Writer, v interface{}) error {
	encoder := yaml.NewEncoder(w)
	encoder.SetIndent(4)
	return encoder.Encode(v)
}

// TOMLHandler handles TOML front matter
type TOMLHandler struct{}

func (h TOMLHandler) Unmarshal(data []byte, v interface{}) error {
	return toml.Unmarshal(data, v)
}

func (h TOMLHandler) Marshal(w io.Writer, v interface{}) error {
	return toml.NewEncoder(w).Encode(v)
}

// formatHandlers maps format types to their handlers
var formatHandlers = map[Format]FormatHandler{
	FormatYAML: YAMLHandler{},
	FormatTOML: TOMLHandler{},
}

// keyMappings defines the mapping of front matter keys for different directions
var keyMappings = map[Direction]map[string]string{
	DirectionHexoToHugo: {
		"permalink": "slug",
		"updated":   "lastmod",
		"sticky":    "weight",
	},
	DirectionHugoToHexo: {
		"slug":    "permalink",
		"lastmod": "updated",
		"weight":  "sticky",
	},
}

// FrontMatterConverter handles front matter conversion
type FrontMatterConverter struct {
	keyMap       map[string]string
	sourceFormat Format
	targetFormat Format
}

// NewFrontMatterConverter creates a new front matter converter with validation
func NewFrontMatterConverter(cfg *Config) (*FrontMatterConverter, error) {
	if _, ok := formatHandlers[cfg.SourceFormat]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedFormat, cfg.SourceFormat)
	}
	if _, ok := formatHandlers[cfg.TargetFormat]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedFormat, cfg.TargetFormat)
	}

	return &FrontMatterConverter{
		keyMap:       keyMappings[cfg.ConversionDirection],
		sourceFormat: cfg.SourceFormat,
		targetFormat: cfg.TargetFormat,
	}, nil
}

// ConvertFrontMatter converts the front matter between formats
func (fmc *FrontMatterConverter) ConvertFrontMatter(frontMatter string) (string, error) {
	frontMatterMap := make(map[string]interface{})

	// Parse the source format
	sourceHandler := formatHandlers[fmc.sourceFormat]
	if err := sourceHandler.Unmarshal([]byte(frontMatter), &frontMatterMap); err != nil {
		return "", fmt.Errorf("unmarshaling front matter: %w", err)
	}

	// Apply key mappings
	convertedMap := make(map[string]interface{}, len(frontMatterMap))
	for key, value := range frontMatterMap {
		targetKey := key
		if mapped, ok := fmc.keyMap[key]; ok {
			targetKey = mapped
		}
		convertedMap[targetKey] = value
	}

	// Marshal to target format
	var buf bytes.Buffer
	targetHandler := formatHandlers[fmc.targetFormat]
	if err := targetHandler.Marshal(&buf, convertedMap); err != nil {
		return "", fmt.Errorf("marshaling front matter: %w", err)
	}

	// Format with delimiters
	return fmt.Sprintf("%s\n%s%s", FrontMatterDelimiter, buf.String(), FrontMatterDelimiter), nil
}

// MarkdownConverter handles the conversion of Markdown files
type MarkdownConverter struct {
	fmc *FrontMatterConverter
}

// NewMarkdownConverter creates a new Markdown converter
func NewMarkdownConverter(cfg *Config) (*MarkdownConverter, error) {
	fmc, err := NewFrontMatterConverter(cfg)
	if err != nil {
		return nil, err
	}
	return &MarkdownConverter{fmc: fmc}, nil
}

// ConvertMarkdown converts a single Markdown file
func (mc *MarkdownConverter) ConvertMarkdown(r io.Reader, w io.Writer) error {
	content, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("reading content: %w", err)
	}

	parts := strings.SplitN(string(content), FrontMatterDelimiter, 3)
	if len(parts) < 3 {
		return ErrInvalidMarkdown
	}

	convertedFrontMatter, err := mc.fmc.ConvertFrontMatter(parts[1])
	if err != nil {
		return fmt.Errorf("converting front matter: %w", err)
	}

	_, err = fmt.Fprintf(w, "%s\n\n%s", convertedFrontMatter, parts[2])
	return err
}

// FileProcessor encapsulates the logic for processing individual files
type FileProcessor struct {
	converter *MarkdownConverter
	srcDir    string
	dstDir    string
	fileExt   string
}

// NewFileProcessor creates a new file processor
func NewFileProcessor(converter *MarkdownConverter, srcDir, dstDir, fileExt string) *FileProcessor {
	return &FileProcessor{
		converter: converter,
		srcDir:    srcDir,
		dstDir:    dstDir,
		fileExt:   fileExt,
	}
}

// ProcessFile handles the conversion of a single file
func (fp *FileProcessor) ProcessFile(ctx context.Context, path string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Skip non-matching files
	if !strings.HasSuffix(path, fp.fileExt) {
		return nil
	}

	// Determine destination path
	relPath, err := filepath.Rel(fp.srcDir, path)
	if err != nil {
		return fmt.Errorf("getting relative path: %w", err)
	}
	dstPath := filepath.Join(fp.dstDir, relPath)

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	// Open source file
	srcFile, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening source file: %w", err)
	}
	defer srcFile.Close()

	// Create destination file
	dstFile, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("creating destination file: %w", err)
	}
	defer func() {
		dstFile.Close()
		if err != nil {
			os.Remove(dstPath)
		}
	}()

	// Convert content
	if err := fp.converter.ConvertMarkdown(srcFile, dstFile); err != nil {
		return fmt.Errorf("converting file: %w", err)
	}

	return nil
}

// ConvertPosts converts all Markdown posts from the source directory to the target format
func ConvertPosts(srcDir, dstDir string, cfg *Config) error {
	if cfg == nil {
		cfg = NewDefaultConfig()
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return fmt.Errorf("creating destination directory %s: %w", dstDir, err)
	}

	// Create converter
	converter, err := NewMarkdownConverter(cfg)
	if err != nil {
		return fmt.Errorf("creating markdown converter: %w", err)
	}

	// Create file processor
	processor := NewFileProcessor(converter, srcDir, dstDir, cfg.FileExtension)

	// Setup error handling
	var (
		mu               sync.Mutex
		conversionErrors []*ConversionError
	)

	// Setup errgroup for concurrent processing
	g, ctx := errgroup.WithContext(context.Background())
	g.SetLimit(cfg.MaxConcurrency)

	// Walk source directory
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}

		// Process each file concurrently
		g.Go(func() error {
			if err := processor.ProcessFile(ctx, path); err != nil {
				mu.Lock()
				conversionErrors = append(conversionErrors, &ConversionError{SourceFile: path, Err: err})
				mu.Unlock()
			}
			return nil
		})

		return nil
	})

	if err != nil {
		return fmt.Errorf("walking source directory %s: %w", srcDir, err)
	}

	// Wait for all goroutines to complete
	if err := g.Wait(); err != nil {
		return err
	}

	// Report errors if any
	if len(conversionErrors) > 0 {
		for _, err := range conversionErrors {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return fmt.Errorf("encountered %d errors during conversion", len(conversionErrors))
	}

	return nil
}
