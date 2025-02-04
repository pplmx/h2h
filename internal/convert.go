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
	var frontMatterMap map[string]interface{}
	if err := unmarshalFrontMatter(fmc.sourceFormat, []byte(frontMatter), &frontMatterMap); err != nil {
		return "", fmt.Errorf("unmarshaling front matter: %w", err)
	}

	convertedMap := make(map[string]interface{}, len(frontMatterMap))
	for key, value := range frontMatterMap {
		if convertedKey, ok := fmc.keyMap[key]; ok {
			convertedMap[convertedKey] = value
		} else {
			convertedMap[key] = value
		}
	}

	var buf bytes.Buffer
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
	content, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("reading content: %w", err)
	}

	parts := strings.SplitN(string(content), "---", 3)
	if len(parts) < 3 {
		return errors.New("parsing content: invalid hexo/hugo markdown format")
	}

	convertedFrontMatter, err := mc.fmc.ConvertFrontMatter(parts[1])
	if err != nil {
		return fmt.Errorf("converting front matter: %w", err)
	}

	_, err = fmt.Fprintf(w, "%s\n\n%s", convertedFrontMatter, parts[2])
	return err
}

// ConversionError represents an error that occurred during the conversion process
type ConversionError struct {
	SourceFile string
	Err        error
}

func (e *ConversionError) Error() string {
	return fmt.Sprintf("converting file %s: %v", e.SourceFile, e.Err)
}

// ConvertPosts converts all markdown posts in the source directory to the target format
func ConvertPosts(srcDir, dstDir string, cfg *Config) error {
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return fmt.Errorf("creating destination directory %s: %w", dstDir, err)
	}

	mc := NewMarkdownConverter(cfg)

	var mu sync.Mutex
	var conversionErrors []*ConversionError

	g, ctx := errgroup.WithContext(context.Background())
	g.SetLimit(cfg.MaxConcurrency)

	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
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
			fmt.Printf("Error: %v\n", err)
		}
		return fmt.Errorf("encountered %d errors during conversion", len(conversionErrors))
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
	defer dstFile.Close()

	if err := mc.ConvertMarkdown(srcFile, dstFile); err != nil {
		os.Remove(dstPath)
		return fmt.Errorf("converting file: %w", err)
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
		return fmt.Errorf("unsupported front matter format: %s", format)
	}
}

func marshalFrontMatter(format string, w io.Writer, v interface{}) error {
	switch format {
	case "yaml":
		encoder := yaml.NewEncoder(w)
		encoder.SetIndent(4)
		return encoder.Encode(v)
	case "toml":
		return toml.NewEncoder(w).Encode(v)
	default:
		return fmt.Errorf("unsupported front matter format: %s", format)
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
	hexoToHugo := getHexoToHugoKeyMap()
	hugoToHexo := make(map[string]string, len(hexoToHugo))
	for hexo, hugo := range hexoToHugo {
		hugoToHexo[hugo] = hexo
	}
	return hugoToHexo
}
