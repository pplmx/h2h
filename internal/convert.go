package internal

import (
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

	"github.com/BurntSushi/toml"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"
)

// 定义常量和错误
const (
	DirectionHexoToHugo Direction = "hexo2hugo"
	DirectionHugoToHexo Direction = "hugo2hexo"

	FormatYAML Format = "yaml"
	FormatTOML Format = "toml"

	DefaultFileExtension = ".md"
	FrontMatterDelimiter = "---"
)

var (
	ErrInvalidMarkdown   = errors.New("invalid markdown: missing front matter delimiters")
	ErrUnsupportedFormat = errors.New("unsupported format")
)

// 基本类型定义
type (
	Direction string
	Format    string

	Config struct {
		SourceFormat        Format
		TargetFormat        Format
		FileExtension       string
		MaxConcurrency      int
		ConversionDirection Direction
	}

	ConversionError struct {
		SourceFile string
		Err        error
	}

	FormatHandler interface {
		Unmarshal(data []byte, v interface{}) error
		Marshal(w io.Writer, v interface{}) error
	}
)

// 错误处理
func (e *ConversionError) Error() string {
	return fmt.Sprintf("converting file %s: %v", e.SourceFile, e.Err)
}

func (e *ConversionError) Unwrap() error {
	return e.Err
}

// 格式处理实现
type (
	YAMLHandler struct{}
	TOMLHandler struct{}
)

func (h YAMLHandler) Unmarshal(data []byte, v interface{}) error {
	return yaml.Unmarshal(data, v)
}

func (h YAMLHandler) Marshal(w io.Writer, v interface{}) error {
	encoder := yaml.NewEncoder(w)
	defer encoder.Close() // 防止内存泄漏
	encoder.SetIndent(4)  // 减少缩进以节省空间
	return encoder.Encode(v)
}

func (h TOMLHandler) Unmarshal(data []byte, v interface{}) error {
	return toml.Unmarshal(data, v)
}

func (h TOMLHandler) Marshal(w io.Writer, v interface{}) error {
	return toml.NewEncoder(w).Encode(v)
}

// 全局变量初始化
var (
	// 格式处理器映射
	formatHandlers = map[Format]FormatHandler{
		FormatYAML: YAMLHandler{},
		FormatTOML: TOMLHandler{},
	}

	// 键映射定义
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

// NewDefaultConfig 返回默认配置
func NewDefaultConfig() *Config {
	return &Config{
		SourceFormat:        FormatYAML,
		TargetFormat:        FormatYAML,
		FileExtension:       DefaultFileExtension,
		MaxConcurrency:      runtime.NumCPU(),
		ConversionDirection: DirectionHexoToHugo,
	}
}

// FrontMatterConverter 处理前置元数据转换
type FrontMatterConverter struct {
	keyMap        map[string]string
	sourceFormat  Format
	targetFormat  Format
	sourceHandler FormatHandler
	targetHandler FormatHandler
}

// NewFrontMatterConverter 创建新的前置元数据转换器
func NewFrontMatterConverter(cfg *Config) (*FrontMatterConverter, error) {
	sourceHandler, ok := formatHandlers[cfg.SourceFormat]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedFormat, cfg.SourceFormat)
	}

	targetHandler, ok := formatHandlers[cfg.TargetFormat]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedFormat, cfg.TargetFormat)
	}

	return &FrontMatterConverter{
		keyMap:        keyMappings[cfg.ConversionDirection],
		sourceFormat:  cfg.SourceFormat,
		targetFormat:  cfg.TargetFormat,
		sourceHandler: sourceHandler,
		targetHandler: targetHandler,
	}, nil
}

// ConvertFrontMatter 在格式之间转换前置元数据
func (fmc *FrontMatterConverter) ConvertFrontMatter(frontMatter string) (string, error) {
	frontMatterMap := make(map[string]interface{})

	// 解析源格式
	if err := fmc.sourceHandler.Unmarshal([]byte(frontMatter), &frontMatterMap); err != nil {
		return "", fmt.Errorf("unmarshaling front matter: %w", err)
	}

	// 应用键映射
	convertedMap := make(map[string]interface{}, len(frontMatterMap))
	for key, value := range frontMatterMap {
		targetKey := key
		if mapped, ok := fmc.keyMap[key]; ok {
			targetKey = mapped
		}
		convertedMap[targetKey] = value
	}

	// 转换为目标格式
	var buf bytes.Buffer
	if err := fmc.targetHandler.Marshal(&buf, convertedMap); err != nil {
		return "", fmt.Errorf("marshaling front matter: %w", err)
	}

	return fmt.Sprintf("%s\n%s%s", FrontMatterDelimiter, buf.String(), FrontMatterDelimiter), nil
}

// MarkdownConverter 处理Markdown文件转换
type MarkdownConverter struct {
	fmc *FrontMatterConverter
}

// NewMarkdownConverter 创建新的Markdown转换器
func NewMarkdownConverter(cfg *Config) (*MarkdownConverter, error) {
	fmc, err := NewFrontMatterConverter(cfg)
	if err != nil {
		return nil, err
	}
	return &MarkdownConverter{fmc: fmc}, nil
}

// ConvertMarkdown 转换单个Markdown文件
func (mc *MarkdownConverter) ConvertMarkdown(r io.Reader, w io.Writer) error {
	content, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("reading content: %w", err)
	}

	parts := strings.SplitN(string(content), FrontMatterDelimiter, 3)
	if len(parts) < 3 {
		return ErrInvalidMarkdown
	}

	convertedFrontMatter, err := mc.fmc.ConvertFrontMatter(strings.TrimSpace(parts[1]))
	if err != nil {
		return fmt.Errorf("converting front matter: %w", err)
	}

	_, err = fmt.Fprintf(w, "%s\n\n%s", convertedFrontMatter, parts[2])
	return err
}

// FileProcessor 封装处理单个文件的逻辑
type FileProcessor struct {
	converter *MarkdownConverter
	srcDir    string
	dstDir    string
	fileExt   string
}

// NewFileProcessor 创建新的文件处理器
func NewFileProcessor(converter *MarkdownConverter, srcDir, dstDir, fileExt string) *FileProcessor {
	return &FileProcessor{
		converter: converter,
		srcDir:    srcDir,
		dstDir:    dstDir,
		fileExt:   fileExt,
	}
}

// ProcessFile 处理单个文件的转换
func (fp *FileProcessor) ProcessFile(ctx context.Context, path string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// 跳过不匹配的文件
	if !strings.HasSuffix(path, fp.fileExt) {
		return nil
	}

	// 确定目标路径
	relPath, err := filepath.Rel(fp.srcDir, path)
	if err != nil {
		return fmt.Errorf("getting relative path: %w", err)
	}
	dstPath := filepath.Join(fp.dstDir, relPath)

	// 确保目标目录存在
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	// 打开源文件
	srcFile, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening source file: %w", err)
	}
	defer srcFile.Close()

	// 创建目标文件
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

	// 转换内容
	return fp.converter.ConvertMarkdown(srcFile, dstFile)
}

// ConvertPosts 将源目录中的所有Markdown文章转换为目标格式
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

	// 记录处理的文件数
	var fileCount int64

	// 遍历源目录
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		if !strings.HasSuffix(path, cfg.FileExtension) {
			return nil
		}

		// 文件计数增加
		fileCount++

		// 并发处理每个文件
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

	// 报告结果
	fmt.Printf("Processed %d files\n", fileCount)

	// 报告错误（如果有）
	if len(conversionErrors) > 0 {
		for _, err := range conversionErrors {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return fmt.Errorf("encountered %d errors during conversion", len(conversionErrors))
	}

	return nil
}
