package internal

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/BurntSushi/toml"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"
)

// Direction represents the conversion direction
type Direction string

// Format represents the front matter format
type Format string

// Conversion directions and formats
const (
	DirectionHexoToHugo Direction = "hexo2hugo"
	DirectionHugoToHexo Direction = "hugo2hexo"

	FormatYAML Format = "yaml"
	FormatTOML Format = "toml"

	DefaultFileExtension = ".md"
	FrontMatterDelimiter = "---"
)

// Common errors
var (
	ErrInvalidMarkdown   = errors.New("invalid markdown: missing front matter delimiters")
	ErrUnsupportedFormat = errors.New("unsupported format")
)

// Config holds the conversion configuration
type Config struct {
	SourceFormat        Format
	TargetFormat        Format
	FileExtension       string
	MaxConcurrency      int
	ConversionDirection Direction
}

// ConversionError wraps errors that occur during conversion
type ConversionError struct {
	SourceFile string
	Err        error
}

// FormatHandler interface for handling different front matter formats
type FormatHandler interface {
	Unmarshal(data []byte, v interface{}) error
	Marshal(w io.Writer, v interface{}) error
}

// Error returns the error string
func (e *ConversionError) Error() string {
	return fmt.Sprintf("converting file %s: %v", e.SourceFile, e.Err)
}

// Unwrap returns the wrapped error
func (e *ConversionError) Unwrap() error {
	return e.Err
}

// YAMLHandler implements FormatHandler for YAML
type YAMLHandler struct{}

// TOMLHandler implements FormatHandler for TOML
type TOMLHandler struct{}

// Unmarshal parses YAML data
func (h YAMLHandler) Unmarshal(data []byte, v interface{}) error {
	return yaml.Unmarshal(data, v)
}

// Marshal serializes data to YAML
func (h YAMLHandler) Marshal(w io.Writer, v interface{}) error {
	encoder := yaml.NewEncoder(w)
	defer encoder.Close()
	encoder.SetIndent(4)
	return encoder.Encode(v)
}

// Unmarshal parses TOML data
func (h TOMLHandler) Unmarshal(data []byte, v interface{}) error {
	return toml.Unmarshal(data, v)
}

// Marshal serializes data to TOML
func (h TOMLHandler) Marshal(w io.Writer, v interface{}) error {
	return toml.NewEncoder(w).Encode(v)
}

// Pre-initialized format handlers and key mappings
var (
	formatHandlers = map[Format]FormatHandler{
		FormatYAML: YAMLHandler{},
		FormatTOML: TOMLHandler{},
	}

	keyMappings = map[Direction]map[string]string{
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
)

// NewDefaultConfig returns a default configuration
func NewDefaultConfig() *Config {
	return &Config{
		SourceFormat:        FormatYAML,
		TargetFormat:        FormatYAML,
		FileExtension:       DefaultFileExtension,
		MaxConcurrency:      runtime.NumCPU(),
		ConversionDirection: DirectionHexoToHugo,
	}
}

// FrontMatterConverter handles front matter conversion
type FrontMatterConverter struct {
	keyMap        map[string]string
	sourceFormat  Format
	targetFormat  Format
	sourceHandler FormatHandler
	targetHandler FormatHandler
}

// NewFrontMatterConverter creates a new FrontMatterConverter
func NewFrontMatterConverter(cfg *Config) (*FrontMatterConverter, error) {
	sourceHandler, ok := formatHandlers[cfg.SourceFormat]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedFormat, cfg.SourceFormat)
	}

	targetHandler, ok := formatHandlers[cfg.TargetFormat]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedFormat, cfg.TargetFormat)
	}

	keyMap := keyMappings[cfg.ConversionDirection]
	if keyMap == nil {
		keyMap = make(map[string]string)
	}

	return &FrontMatterConverter{
		keyMap:        keyMap,
		sourceFormat:  cfg.SourceFormat,
		targetFormat:  cfg.TargetFormat,
		sourceHandler: sourceHandler,
		targetHandler: targetHandler,
	}, nil
}

// ConvertFrontMatter converts front matter between formats
func (fmc *FrontMatterConverter) ConvertFrontMatter(frontMatter string) (string, error) {
	frontMatterMap := make(map[string]interface{})

	// Parse source format
	if err := fmc.sourceHandler.Unmarshal([]byte(frontMatter), &frontMatterMap); err != nil {
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

	// Convert to target format
	var buf bytes.Buffer
	if err := fmc.targetHandler.Marshal(&buf, convertedMap); err != nil {
		return "", fmt.Errorf("marshaling front matter: %w", err)
	}

	return fmt.Sprintf("%s\n%s%s", FrontMatterDelimiter, buf.String(), FrontMatterDelimiter), nil
}

// MarkdownConverter handles Markdown file conversion
type MarkdownConverter struct {
	fmc *FrontMatterConverter
}

// NewMarkdownConverter creates a new MarkdownConverter
func NewMarkdownConverter(cfg *Config) (*MarkdownConverter, error) {
	fmc, err := NewFrontMatterConverter(cfg)
	if err != nil {
		return nil, err
	}
	return &MarkdownConverter{fmc: fmc}, nil
}

// ConvertMarkdown converts a single Markdown file
func (mc *MarkdownConverter) ConvertMarkdown(r io.Reader, w io.Writer) error {
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		return fmt.Errorf("reading content: %w", err)
	}

	content := buf.String()
	parts := strings.SplitN(content, FrontMatterDelimiter, 3)
	if len(parts) < 3 {
		return ErrInvalidMarkdown
	}

	convertedFrontMatter, err := mc.fmc.ConvertFrontMatter(strings.TrimSpace(parts[1]))
	if err != nil {
		return fmt.Errorf("converting front matter: %w", err)
	}

	writer := bufio.NewWriter(w)
	if _, err := writer.WriteString(convertedFrontMatter); err != nil {
		return err
	}

	if _, err := writer.WriteString("\n\n"); err != nil {
		return err
	}

	if _, err := writer.WriteString(parts[2]); err != nil {
		return err
	}

	return writer.Flush()
}

// FileProcessor encapsulates logic for processing a single file
type FileProcessor struct {
	converter *MarkdownConverter
	srcDir    string
	dstDir    string
	fileExt   string
}

// NewFileProcessor creates a new FileProcessor
func NewFileProcessor(converter *MarkdownConverter, srcDir, dstDir, fileExt string) *FileProcessor {
	return &FileProcessor{
		converter: converter,
		srcDir:    srcDir,
		dstDir:    dstDir,
		fileExt:   fileExt,
	}
}

// ProcessFile processes a single file conversion
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

	// Determine target path
	relPath, err := filepath.Rel(fp.srcDir, path)
	if err != nil {
		return fmt.Errorf("getting relative path: %w", err)
	}
	dstPath := filepath.Join(fp.dstDir, relPath)

	// Ensure target directory exists
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	// Open source file
	srcFile, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening source file: %w", err)
	}
	defer srcFile.Close()

	// Create target file
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
	bufWriter := bufio.NewWriter(dstFile)
	err = fp.converter.ConvertMarkdown(srcFile, bufWriter)
	if err != nil {
		return err
	}
	return bufWriter.Flush()
}

// ConvertPosts converts all Markdown posts in the source directory to the target format
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

	// Track processed files count
	var fileCount atomic.Int64

	// Collect matching files first to avoid file system bottlenecks
	var files []string
	err = filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		if strings.HasSuffix(path, cfg.FileExtension) {
			files = append(files, path)
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("walking source directory %s: %w", srcDir, err)
	}

	// Process files concurrently
	for _, path := range files {
		path := path // Capture loop variable
		g.Go(func() error {
			if err := processor.ProcessFile(ctx, path); err != nil {
				mu.Lock()
				conversionErrors = append(conversionErrors, &ConversionError{SourceFile: path, Err: err})
				mu.Unlock()
				return nil // Continue processing other files
			}
			fileCount.Add(1)
			return nil
		})
	}

	// Wait for all goroutines to complete
	if err := g.Wait(); err != nil {
		return err
	}

	// Report results
	fmt.Printf("Processed %d files\n", fileCount.Load())

	// Report errors (if any)
	if len(conversionErrors) > 0 {
		for _, err := range conversionErrors {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return fmt.Errorf("encountered %d errors during conversion", len(conversionErrors))
	}

	return nil
}
