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

// Constants for configuration and conversion settings
const (
	DefaultMaxConcurrency = 4
	DefaultFileExtension  = ".md"
	FrontMatterDelimiter  = "---"

	DirectionHexoToHugo = "hexo2hugo"
	DirectionHugoToHexo = "hugo2hexo"

	FormatYAML = "yaml"
	FormatTOML = "toml"
)

// Config defines the settings for the conversion process
type Config struct {
	SourceFormat        string
	TargetFormat        string
	FileExtension       string
	MaxConcurrency      int
	ConversionDirection string
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

// ConversionError represents an error during the conversion process
type ConversionError struct {
	SourceFile string
	Err        error
}

func (e *ConversionError) Error() string {
	return fmt.Sprintf("converting file %s: %v", e.SourceFile, e.Err)
}

// Unwrap returns the underlying error for compatibility with errors.Is/As
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

// Map of format handlers
var formatHandlers = map[string]FormatHandler{
	FormatYAML: YAMLHandler{},
	FormatTOML: TOMLHandler{},
}

// KeyMappings defines the mapping of front matter keys for different directions
var keyMappings = map[string]map[string]string{
	DirectionHexoToHugo: {
		"title":       "title",
		"categories":  "categories",
		"date":        "date",
		"description": "description",
		"keywords":    "keywords",
		"permalink":   "slug",
		"tags":        "tags",
		"updated":     "lastmod",
		"sticky":      "weight",
	},
}

// FrontMatterConverter handles front matter conversion
type FrontMatterConverter struct {
	keyMap       map[string]string
	sourceFormat string
	targetFormat string
}

// NewFrontMatterConverter creates a new front matter converter
func NewFrontMatterConverter(cfg *Config) (*FrontMatterConverter, error) {
	if _, ok := formatHandlers[cfg.SourceFormat]; !ok {
		return nil, fmt.Errorf("unsupported source format: %s", cfg.SourceFormat)
	}
	if _, ok := formatHandlers[cfg.TargetFormat]; !ok {
		return nil, fmt.Errorf("unsupported target format: %s", cfg.TargetFormat)
	}

	return &FrontMatterConverter{
		keyMap:       getKeyMap(cfg.ConversionDirection),
		sourceFormat: cfg.SourceFormat,
		targetFormat: cfg.TargetFormat,
	}, nil
}

// getKeyMap retrieves the key mapping for the specified direction
func getKeyMap(direction string) map[string]string {
	if direction == DirectionHexoToHugo {
		return keyMappings[DirectionHexoToHugo]
	}

	// Reverse mapping for hugo2hexo
	hugoToHexo := make(map[string]string, len(keyMappings[DirectionHexoToHugo]))
	for hexo, hugo := range keyMappings[DirectionHexoToHugo] {
		hugoToHexo[hugo] = hexo
	}
	return hugoToHexo
}

// ConvertFrontMatter converts the front matter between formats
func (fmc *FrontMatterConverter) ConvertFrontMatter(frontMatter string) (string, error) {
	frontMatterMap := make(map[string]interface{})

	handler := formatHandlers[fmc.sourceFormat]
	if err := handler.Unmarshal([]byte(frontMatter), &frontMatterMap); err != nil {
		return "", fmt.Errorf("unmarshaling front matter: %w", err)
	}

	convertedMap := make(map[string]interface{}, len(frontMatterMap))
	for key, value := range frontMatterMap {
		targetKey := key
		if converted, ok := fmc.keyMap[key]; ok {
			targetKey = converted
		}
		convertedMap[targetKey] = value
	}

	var buf bytes.Buffer
	if err := formatHandlers[fmc.targetFormat].Marshal(&buf, convertedMap); err != nil {
		return "", fmt.Errorf("marshaling front matter: %w", err)
	}

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

// ErrInvalidMarkdown indicates missing front matter delimiters
var ErrInvalidMarkdown = errors.New("invalid markdown format: missing front matter delimiters")

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

// convertFile handles the conversion of a single file
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
	defer func() {
		dstFile.Close()
		if err != nil {
			os.Remove(dstPath)
		}
	}()

	if err := mc.ConvertMarkdown(srcFile, dstFile); err != nil {
		return fmt.Errorf("converting file: %w", err)
	}

	return nil
}

// ConvertPosts converts all Markdown posts from the source directory to the target format
func ConvertPosts(srcDir, dstDir string, cfg *Config) error {
	if cfg == nil {
		cfg = NewDefaultConfig()
	}

	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return fmt.Errorf("creating destination directory %s: %w", dstDir, err)
	}

	mc, err := NewMarkdownConverter(cfg)
	if err != nil {
		return fmt.Errorf("creating markdown converter: %w", err)
	}

	var (
		mu               sync.Mutex
		conversionErrors []*ConversionError
	)

	g, ctx := errgroup.WithContext(context.Background())
	g.SetLimit(cfg.MaxConcurrency)

	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(info.Name(), cfg.FileExtension) {
			return err
		}

		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return fmt.Errorf("getting relative path: %w", err)
		}
		dstPath := filepath.Join(dstDir, relPath)

		g.Go(func() error {
			if err := convertFile(ctx, mc, path, dstPath); err != nil {
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

	if err := g.Wait(); err != nil {
		return err
	}

	if len(conversionErrors) > 0 {
		for _, err := range conversionErrors {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return fmt.Errorf("encountered %d errors during conversion", len(conversionErrors))
	}

	return nil
}
