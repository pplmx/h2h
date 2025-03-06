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
	"strings"
	"sync"
	"sync/atomic"

	"github.com/BurntSushi/toml"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"
)

// Errors
var (
	ErrInvalidMarkdownFormat = errors.New("invalid markdown format: missing front matter delimiters")
	ErrUnsupportedFormat     = errors.New("unsupported front matter format")
)

// Config holds the configuration for the conversion process
type Config struct {
	SourceFormat        string
	TargetFormat        string
	FileExtension       string
	MaxConcurrency      int
	ConversionDirection string
}

// NewDefaultConfig returns a default configuration
func NewDefaultConfig() *Config {
	return &Config{
		SourceFormat:        "yaml",
		TargetFormat:        "yaml",
		FileExtension:       ".md",
		MaxConcurrency:      4,
		ConversionDirection: "hexo2hugo",
	}
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	// Validate formats
	for _, format := range []string{c.SourceFormat, c.TargetFormat} {
		if format != "yaml" && format != "toml" {
			return fmt.Errorf("%w: %s", ErrUnsupportedFormat, format)
		}
	}

	// Validate conversion direction
	if c.ConversionDirection != "hexo2hugo" && c.ConversionDirection != "hugo2hexo" {
		return fmt.Errorf("invalid conversion direction: %s", c.ConversionDirection)
	}

	return nil
}

// FrontMatterConverter handles the conversion of front matter
type FrontMatterConverter struct {
	keyMap       map[string]string
	sourceFormat string
	targetFormat string
}

// NewFrontMatterConverter creates a new FrontMatterConverter
func NewFrontMatterConverter(cfg *Config) *FrontMatterConverter {
	var keyMap map[string]string
	if cfg.ConversionDirection == "hexo2hugo" {
		keyMap = getHexoToHugoKeyMap()
	} else {
		keyMap = getHugoToHexoKeyMap()
	}

	return &FrontMatterConverter{
		keyMap:       keyMap,
		sourceFormat: cfg.SourceFormat,
		targetFormat: cfg.TargetFormat,
	}
}

// ConvertFrontMatter converts the front matter from source format to target format
func (fmc *FrontMatterConverter) ConvertFrontMatter(frontMatter string) (string, error) {
	frontMatter = strings.TrimSpace(frontMatter)

	var frontMatterMap map[string]interface{}
	if err := unmarshalFrontMatter(fmc.sourceFormat, []byte(frontMatter), &frontMatterMap); err != nil {
		return "", fmt.Errorf("unmarshaling front matter: %w", err)
	}

	// Pre-allocate with the same size for efficiency
	convertedMap := make(map[string]interface{}, len(frontMatterMap))
	for key, value := range frontMatterMap {
		if convertedKey, ok := fmc.keyMap[key]; ok {
			convertedMap[convertedKey] = value
		} else {
			convertedMap[key] = value
		}
	}

	var buf bytes.Buffer
	buf.Grow(len(frontMatter) + 100) // Pre-allocate buffer with estimated size

	if err := marshalFrontMatter(fmc.targetFormat, &buf, convertedMap); err != nil {
		return "", fmt.Errorf("marshaling front matter: %w", err)
	}

	return fmt.Sprintf("---\n%s---", buf.String()), nil
}

// MarkdownConverter handles the conversion of markdown files
type MarkdownConverter struct {
	fmc *FrontMatterConverter
}

// NewMarkdownConverter creates a new MarkdownConverter
func NewMarkdownConverter(cfg *Config) *MarkdownConverter {
	return &MarkdownConverter{fmc: NewFrontMatterConverter(cfg)}
}

// ConvertMarkdown converts a single markdown file
func (mc *MarkdownConverter) ConvertMarkdown(r io.Reader, w io.Writer) error {
	// Use buffered reader for better performance
	br := bufio.NewReader(r)

	// Read the first delimiter
	firstLine, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(strings.TrimSpace(firstLine), "---") {
		return ErrInvalidMarkdownFormat
	}

	// Read the front matter content
	var frontMatter strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return fmt.Errorf("reading front matter: %w", err)
		}

		if strings.HasPrefix(strings.TrimSpace(line), "---") {
			break
		}

		frontMatter.WriteString(line)

		if err == io.EOF {
			return ErrInvalidMarkdownFormat
		}
	}

	// Convert the front matter
	convertedFrontMatter, err := mc.fmc.ConvertFrontMatter(frontMatter.String())
	if err != nil {
		return fmt.Errorf("converting front matter: %w", err)
	}

	// Write the converted front matter
	if _, err := fmt.Fprintf(w, "%s\n\n", convertedFrontMatter); err != nil {
		return fmt.Errorf("writing front matter: %w", err)
	}

	// Copy the rest of the content
	if _, err := io.Copy(w, br); err != nil {
		return fmt.Errorf("writing content: %w", err)
	}

	return nil
}

// ConversionError represents an error that occurred during the conversion process
type ConversionError struct {
	SourceFile string
	Err        error
}

func (e *ConversionError) Error() string {
	return fmt.Sprintf("converting file %s: %v", e.SourceFile, e.Err)
}

// ConversionStats tracks statistics about the conversion process
type ConversionStats struct {
	FilesProcessed uint64
	FilesConverted uint64
	Errors         []*ConversionError
	mu             sync.Mutex
}

// AddError adds an error to the statistics
func (s *ConversionStats) AddError(err *ConversionError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Errors = append(s.Errors, err)
}

// FileProcessed increments the count of processed files
func (s *ConversionStats) FileProcessed() {
	atomic.AddUint64(&s.FilesProcessed, 1)
}

// FileConverted increments the count of successfully converted files
func (s *ConversionStats) FileConverted() {
	atomic.AddUint64(&s.FilesConverted, 1)
}

// PrintSummary prints a summary of the conversion process
func (s *ConversionStats) PrintSummary() {
	fmt.Printf("Files processed: %d\n", s.FilesProcessed)
	fmt.Printf("Files successfully converted: %d\n", s.FilesConverted)

	if len(s.Errors) > 0 {
		fmt.Printf("Errors occurred during conversion: %d\n", len(s.Errors))
		for _, err := range s.Errors {
			fmt.Printf("- %v\n", err)
		}
	}
}

// ConvertPosts converts all markdown posts in the source directory to the target format
func ConvertPosts(srcDir, dstDir string, cfg *Config) error {
	// Validate the configuration
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return fmt.Errorf("creating destination directory %s: %w", dstDir, err)
	}

	mc := NewMarkdownConverter(cfg)
	stats := &ConversionStats{}

	g, ctx := errgroup.WithContext(context.Background())
	g.SetLimit(cfg.MaxConcurrency)

	fileListChan := make(chan string, 100) // Buffer channel to improve performance

	// Start a goroutine to walk the directory and send files to the channel
	g.Go(func() error {
		defer close(fileListChan)

		return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if info.IsDir() || !strings.HasSuffix(info.Name(), cfg.FileExtension) {
				return nil
			}

			select {
			case fileListChan <- path:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	})

	// Start worker goroutines to process files
	for i := 0; i < cfg.MaxConcurrency; i++ {
		g.Go(func() error {
			for srcPath := range fileListChan {
				stats.FileProcessed()

				relPath, err := filepath.Rel(srcDir, srcPath)
				if err != nil {
					stats.AddError(&ConversionError{SourceFile: srcPath, Err: fmt.Errorf("getting relative path: %w", err)})
					continue
				}

				dstPath := filepath.Join(dstDir, relPath)

				if err := convertFile(ctx, mc, srcPath, dstPath); err != nil {
					stats.AddError(&ConversionError{SourceFile: srcPath, Err: err})
				} else {
					stats.FileConverted()
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return err
	}

	stats.PrintSummary()

	if len(stats.Errors) > 0 {
		return fmt.Errorf("encountered %d errors during conversion", len(stats.Errors))
	}

	return nil
}

func convertFile(ctx context.Context, mc *MarkdownConverter, srcPath, dstPath string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	srcFile, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("opening source file: %w", err)
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	dstFile, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("creating destination file: %w", err)
	}

	// Use buffered writer for better performance
	bw := bufio.NewWriter(dstFile)

	err = mc.ConvertMarkdown(srcFile, bw)
	if flushErr := bw.Flush(); flushErr != nil && err == nil {
		err = fmt.Errorf("flushing output: %w", flushErr)
	}

	closeErr := dstFile.Close()
	if err != nil {
		os.Remove(dstPath) // Clean up on conversion error
		return fmt.Errorf("converting file: %w", err)
	}

	if closeErr != nil {
		return fmt.Errorf("closing destination file: %w", closeErr)
	}

	return nil
}

func unmarshalFrontMatter(format string, data []byte, v interface{}) error {
	switch format {
	case "yaml":
		return yaml.Unmarshal(data, v)
	case "toml":
		return toml.Unmarshal(data, v)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedFormat, format)
	}
}

func marshalFrontMatter(format string, w io.Writer, v interface{}) error {
	switch format {
	case "yaml":
		encoder := yaml.NewEncoder(w)
		encoder.SetIndent(4)
		defer encoder.Close() // Properly close encoder
		return encoder.Encode(v)
	case "toml":
		return toml.NewEncoder(w).Encode(v)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedFormat, format)
	}
}

func getHexoToHugoKeyMap() map[string]string {
	return map[string]string{
		"title":       "title",
		"categories":  "categories",
		"date":        "date",
		"description": "description",
		"keywords":    "keywords",
		"permalink":   "slug",
		"tags":        "tags",
		"updated":     "lastmod",
		"sticky":      "weight",
	}
}

func getHugoToHexoKeyMap() map[string]string {
	// Invert the hexo to hugo map
	hexoToHugo := getHexoToHugoKeyMap()
	hugoToHexo := make(map[string]string, len(hexoToHugo))
	for hexo, hugo := range hexoToHugo {
		hugoToHexo[hugo] = hexo
	}
	return hugoToHexo
}
