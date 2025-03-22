package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pplmx/h2h/internal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFile represents a test file with its properties
type TestFile struct {
	Name       string
	Title      string
	Date       string
	Tags       []string
	Categories []string
	Content    string // Additional content beyond the front matter and title
}

// GenerateContent creates the full file content including front matter
func (tf TestFile) GenerateContent() string {
	var sb strings.Builder

	// Front matter
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("title: %s\n", tf.Title))
	sb.WriteString(fmt.Sprintf("date: %s\n", tf.Date))

	if len(tf.Tags) > 0 {
		sb.WriteString(fmt.Sprintf("tags: [%s]\n", strings.Join(tf.Tags, ", ")))
	}
	if len(tf.Categories) > 0 {
		sb.WriteString(fmt.Sprintf("categories: [%s]\n", strings.Join(tf.Categories, ", ")))
	}
	sb.WriteString("---\n")

	// Default content
	sb.WriteString(fmt.Sprintf("# %s\n", tf.Title))

	// If custom content is provided, use it; otherwise use default
	if tf.Content != "" {
		sb.WriteString(tf.Content)
	} else {
		sb.WriteString("This is a test post")
	}

	return sb.String()
}

// TestEnvironment holds the testing environment setup
type TestEnvironment struct {
	T       testing.TB
	SrcDir  string
	DstDir  string
	Files   []TestFile
	Config  *internal.Config
	fileMap map[string]TestFile
}

// NewTestEnvironment creates a new test environment with temporary directories
func NewTestEnvironment(t testing.TB) *TestEnvironment {
	return &TestEnvironment{
		T:       t,
		SrcDir:  t.TempDir(),
		DstDir:  t.TempDir(),
		Config:  internal.NewDefaultConfig(),
		fileMap: make(map[string]TestFile),
	}
}

// AddFile adds a test file to the environment
func (env *TestEnvironment) AddFile(file TestFile) *TestEnvironment {
	env.Files = append(env.Files, file)
	env.fileMap[file.Name] = file
	return env
}

// AddFiles adds multiple test files to the environment
func (env *TestEnvironment) AddFiles(files []TestFile) *TestEnvironment {
	for _, file := range files {
		env.AddFile(file)
	}
	return env
}

// Setup creates the directories and writes test files
func (env *TestEnvironment) Setup() *TestEnvironment {
	for _, file := range env.Files {
		dir := filepath.Dir(filepath.Join(env.SrcDir, file.Name))
		err := os.MkdirAll(dir, 0755)
		require.NoError(env.T, err, "Failed to create directory: %s", dir)

		content := file.GenerateContent()
		err = os.WriteFile(filepath.Join(env.SrcDir, file.Name), []byte(content), 0644)
		require.NoError(env.T, err, "Failed to create test file: %s", file.Name)
	}
	return env
}

// RunConversion executes the conversion process
func (env *TestEnvironment) RunConversion() error {
	return internal.ConvertPosts(env.SrcDir, env.DstDir, env.Config)
}

// VerifyFile checks if a single file was converted properly
func (env *TestEnvironment) VerifyFile(t *testing.T, fileName string, expectedContentSubstr string) {
	t.Helper()

	filePath := filepath.Join(env.DstDir, fileName)
	content, err := os.ReadFile(filePath)
	require.NoError(t, err, "Failed to read converted file %s", fileName)

	assert.Equal(t, 2, strings.Count(string(content), "---"),
		"Expected 2 '---' separators in %s", fileName)
	assert.Contains(t, string(content), expectedContentSubstr,
		"Converted file %s does not contain expected content", fileName)
}

// VerifyAllFiles checks all files for proper conversion
func (env *TestEnvironment) VerifyAllFiles(t *testing.T) {
	t.Helper()
	for _, file := range env.Files {
		env.VerifyFile(t, file.Name, "This is a test post")
	}
}

// NewTestFile creates a TestFile with standard defaults
func NewTestFile(name, title, date string, tags, categories []string) TestFile {
	return TestFile{
		Name:       name,
		Title:      title,
		Date:       date,
		Tags:       tags,
		Categories: categories,
	}
}

// TestConvertPosts tests the post conversion functionality
func TestConvertPosts(t *testing.T) {
	testCases := []struct {
		name         string
		setupEnv     func(*TestEnvironment)
		expectError  bool
		errorMessage string
		verify       func(*testing.T, *TestEnvironment, error)
	}{
		{
			name: "Basic conversion (Hexo2Hugo)",
			setupEnv: func(env *TestEnvironment) {
				env.AddFile(NewTestFile("test1.md", "Test Post 1", "2023-05-01", []string{"test", "markdown"}, nil))
				env.AddFile(NewTestFile("test2.md", "Test Post 2", "2023-05-02", nil, []string{"testing"}))
			},
			expectError: false,
			verify: func(t *testing.T, env *TestEnvironment, err error) {
				assert.NoError(t, err)
				env.VerifyAllFiles(t)
			},
		},
		{
			name: "Invalid front matter",
			setupEnv: func(env *TestEnvironment) {
				env.AddFile(TestFile{
					Name:    "invalid.md",
					Content: "# Invalid Post\nThis is an invalid post without front matter.",
				})
			},
			expectError:  true,
			errorMessage: "encountered 1 errors during conversion",
			verify: func(t *testing.T, env *TestEnvironment, err error) {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "encountered 1 errors during conversion")
			},
		},
		{
			name: "Empty file",
			setupEnv: func(env *TestEnvironment) {
				env.AddFile(TestFile{Name: "empty.md"})
			},
			expectError:  true,
			errorMessage: "encountered 1 errors during conversion",
			verify: func(t *testing.T, env *TestEnvironment, err error) {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "encountered 1 errors during conversion")
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup test environment
			env := NewTestEnvironment(t)
			tc.setupEnv(env)
			env.Setup()

			// Run the conversion
			err := env.RunConversion()

			// Verify results
			tc.verify(t, env, err)
		})
	}
}

// TestEdgeCases tests edge cases in conversion
func TestEdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		setupEnv   func(*TestEnvironment)
		configMods func(*internal.Config)
		verify     func(*testing.T, *TestEnvironment, error)
	}{
		{
			name: "Large file",
			setupEnv: func(env *TestEnvironment) {
				largeFile := NewTestFile("large.md", "Large Post", "2023-05-01", nil, nil)
				largeFile.Content = strings.Repeat("This is a large test post.\n", 10000)
				env.AddFile(largeFile)
			},
			verify: func(t *testing.T, env *TestEnvironment, err error) {
				assert.NoError(t, err, "ConvertPosts failed for large file")
				env.VerifyFile(t, "large.md", "This is a large test post.")
			},
		},
		{
			name: "Nested directories",
			setupEnv: func(env *TestEnvironment) {
				nestedFile := NewTestFile("nested/nested.md", "Nested Post", "2023-05-01", nil, nil)
				nestedFile.Content = "This is a nested post."
				env.AddFile(nestedFile)
			},
			verify: func(t *testing.T, env *TestEnvironment, err error) {
				assert.NoError(t, err, "ConvertPosts failed for nested directories")
				env.VerifyFile(t, "nested/nested.md", "This is a nested post.")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := NewTestEnvironment(t)
			tc.setupEnv(env)

			if tc.configMods != nil {
				tc.configMods(env.Config)
			}

			env.Setup()
			err := env.RunConversion()
			tc.verify(t, env, err)
		})
	}
}

// TestConcurrency tests different concurrency levels
func TestConcurrency(t *testing.T) {
	const fileCount = 10

	concurrencyLevels := []int{1, 2, 4, 8}
	for _, concurrency := range concurrencyLevels {
		t.Run(fmt.Sprintf("Concurrency%d", concurrency), func(t *testing.T) {
			env := NewTestEnvironment(t)
			env.Config.MaxConcurrency = concurrency

			// Generate test files
			for i := 0; i < fileCount; i++ {
				file := NewTestFile(
					fmt.Sprintf("test%d.md", i),
					fmt.Sprintf("Test Post %d", i),
					fmt.Sprintf("2023-05-%02d", i+1),
					nil, nil,
				)
				file.Content = fmt.Sprintf("This is test post number %d.", i)
				env.AddFile(file)
			}
			env.Setup()

			err := env.RunConversion()
			assert.NoError(t, err, "ConvertPosts failed with concurrency %d", concurrency)

			for i := 0; i < fileCount; i++ {
				env.VerifyFile(t, fmt.Sprintf("test%d.md", i), fmt.Sprintf("This is test post number %d.", i))
			}
		})
	}
}

// TestParallelConversions tests running multiple conversions in parallel
func TestParallelConversions(t *testing.T) {
	const envCount = 4
	const fileCount = 5

	var wg sync.WaitGroup
	wg.Add(envCount)

	for e := 0; e < envCount; e++ {
		go func(envNum int) {
			defer wg.Done()

			env := NewTestEnvironment(t)

			// Generate different files for each environment
			for i := 0; i < fileCount; i++ {
				file := NewTestFile(
					fmt.Sprintf("env%d_test%d.md", envNum, i),
					fmt.Sprintf("Env %d Test Post %d", envNum, i),
					fmt.Sprintf("2023-05-%02d", i+1),
					nil, nil,
				)
				file.Content = fmt.Sprintf("This is env %d test post number %d.", envNum, i)
				env.AddFile(file)
			}
			env.Setup()

			err := env.RunConversion()
			assert.NoError(t, err, "ConvertPosts failed for parallel environment %d", envNum)

			// Verify in same goroutine to avoid race conditions
			for i := 0; i < fileCount; i++ {
				content, err := os.ReadFile(filepath.Join(env.DstDir, fmt.Sprintf("env%d_test%d.md", envNum, i)))
				assert.NoError(t, err)
				assert.Contains(t, string(content), fmt.Sprintf("This is env %d test post number %d.", envNum, i))
			}
		}(e)
	}

	wg.Wait()
}

// BenchmarkConvertPosts benchmarks the conversion process
func BenchmarkConvertPosts(b *testing.B) {
	benchmarks := []struct {
		name         string
		fileCount    int
		concurrency  int
		contentLines int
	}{
		{"Small_Sequential", 5, 1, 10},
		{"Small_Parallel", 5, 4, 10},
		{"Medium_Sequential", 20, 1, 50},
		{"Medium_Parallel", 20, 4, 50},
		{"Large_Sequential", 50, 1, 100},
		{"Large_Parallel", 50, 8, 100},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			// Setup once before benchmarking
			env := NewTestEnvironment(b)
			env.Config.MaxConcurrency = bm.concurrency

			// Generate benchmark files
			for i := 0; i < bm.fileCount; i++ {
				file := NewTestFile(
					fmt.Sprintf("bench%d.md", i),
					fmt.Sprintf("Bench Post %d", i),
					fmt.Sprintf("2023-05-%02d", i%30+1),
					nil, nil,
				)
				file.Content = strings.Repeat("This is a benchmark post line.\n", bm.contentLines)
				env.AddFile(file)
			}
			env.Setup()

			// Reset timer before running the actual benchmark
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				err := internal.ConvertPosts(env.SrcDir, env.DstDir, env.Config)
				if err != nil {
					b.Fatalf("ConvertPosts failed: %v", err)
				}
			}
		})
	}
}
