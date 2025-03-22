package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pplmx/h2h/internal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertPosts(t *testing.T) {
	testCases := []struct {
		name         string
		files        []testFile
		config       *internal.Config
		expectError  bool
		errorMessage string
	}{
		{
			name: "Basic conversion (Hexo2Hugo)",
			files: []testFile{
				newTestFile("test1.md", "Test Post 1", "2023-05-01", []string{"test", "markdown"}, nil),
				newTestFile("test2.md", "Test Post 2", "2023-05-02", nil, []string{"testing"}),
			},
			config:      internal.NewDefaultConfig(),
			expectError: false,
		},
		{
			name: "Invalid front matter",
			files: []testFile{
				{name: "invalid.md", content: "# Invalid Post\nThis is an invalid post without front matter."},
			},
			config:       internal.NewDefaultConfig(),
			expectError:  true,
			errorMessage: "encountered 1 errors during conversion",
		},
		{
			name: "Empty file",
			files: []testFile{
				{name: "empty.md", content: ""},
			},
			config:       internal.NewDefaultConfig(),
			expectError:  true,
			errorMessage: "encountered 1 errors during conversion",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup test environment
			srcDir, dstDir := createTestEnvironment(t, tc.files)

			// Run the function under test
			err := internal.ConvertPosts(srcDir, dstDir, tc.config)

			// Verify results
			if tc.expectError {
				assert.Error(t, err)
				if tc.errorMessage != "" {
					assert.Contains(t, err.Error(), tc.errorMessage)
				}
			} else {
				assert.NoError(t, err)
				for _, file := range tc.files {
					verifyConvertedFile(t, dstDir, file)
				}
			}
		})
	}
}

func TestEdgeCases(t *testing.T) {
	tests := map[string]struct {
		setupFn    func(t *testing.T) (srcDir, dstDir string, files []testFile)
		verifyFn   func(t *testing.T, srcDir, dstDir string, files []testFile, err error)
		configMods func(*internal.Config)
	}{
		"Large file": {
			setupFn: func(t *testing.T) (string, string, []testFile) {
				largeFile := newTestFile("large.md", "Large Post", "2023-05-01", nil, nil)
				largeFile.content += strings.Repeat("This is a large test post.\n", 10000)
				srcDir, dstDir := createTestEnvironment(t, []testFile{largeFile})
				return srcDir, dstDir, []testFile{largeFile}
			},
			verifyFn: func(t *testing.T, _, dstDir string, files []testFile, err error) {
				assert.NoError(t, err, "ConvertPosts failed for large file")
				verifyFileContent(t, dstDir, "large.md", "This is a large test post.")
			},
		},
		"Nested directories": {
			setupFn: func(t *testing.T) (string, string, []testFile) {
				nestedFile := newTestFile("nested/nested.md", "Nested Post", "2023-05-01", nil, nil)
				nestedFile.content += "# Nested Post\nThis is a nested post."
				srcDir, dstDir := createTestEnvironment(t, []testFile{nestedFile})
				return srcDir, dstDir, []testFile{nestedFile}
			},
			verifyFn: func(t *testing.T, _, dstDir string, files []testFile, err error) {
				assert.NoError(t, err, "ConvertPosts failed for nested directories")
				verifyFileContent(t, filepath.Join(dstDir, "nested"), "nested.md", "This is a nested post.")
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			// Setup
			srcDir, dstDir, files := tc.setupFn(t)

			// Configure
			cfg := internal.NewDefaultConfig()
			if tc.configMods != nil {
				tc.configMods(cfg)
			}

			// Run
			err := internal.ConvertPosts(srcDir, dstDir, cfg)

			// Verify
			tc.verifyFn(t, srcDir, dstDir, files, err)
		})
	}
}

func TestConcurrency(t *testing.T) {
	const fileCount = 10

	// Generate test files
	files := make([]testFile, fileCount)
	for i := 0; i < fileCount; i++ {
		files[i] = newTestFile(
			fmt.Sprintf("test%d.md", i),
			fmt.Sprintf("Test Post %d", i),
			fmt.Sprintf("2023-05-%02d", i+1),
			nil, nil,
		)
		files[i].content += fmt.Sprintf("# Test Post %d\nThis is test post number %d.", i, i)
	}

	srcDir, dstDir := createTestEnvironment(t, files)

	// Test different concurrency levels
	concurrencyLevels := []int{1, 2, 4, 8}
	for _, concurrency := range concurrencyLevels {
		t.Run(fmt.Sprintf("Concurrency%d", concurrency), func(t *testing.T) {
			cfg := internal.NewDefaultConfig()
			cfg.MaxConcurrency = concurrency

			err := internal.ConvertPosts(srcDir, dstDir, cfg)
			assert.NoError(t, err, "ConvertPosts failed with concurrency %d", concurrency)

			for i := 0; i < fileCount; i++ {
				verifyFileContent(t, dstDir, fmt.Sprintf("test%d.md", i), fmt.Sprintf("This is test post number %d.", i))
			}
		})
	}
}

func BenchmarkConvertPosts(b *testing.B) {
	const fileCount = 10
	const contentRepetitions = 10

	// Generate benchmark files
	files := make([]testFile, fileCount)
	for i := 0; i < fileCount; i++ {
		files[i] = newTestFile(
			fmt.Sprintf("bench%d.md", i),
			fmt.Sprintf("Bench Post %d", i),
			fmt.Sprintf("2023-05-%02d", i%30+1),
			nil, nil,
		)
		files[i].content += fmt.Sprintf("# Bench Post %d\n%s", i,
			strings.Repeat("This is a benchmark post.\n", contentRepetitions))
	}

	srcDir, dstDir := createTestEnvironment(b, files)
	cfg := internal.NewDefaultConfig()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := internal.ConvertPosts(srcDir, dstDir, cfg)
		if err != nil {
			b.Fatalf("ConvertPosts failed: %v", err)
		}
	}
}

// Types and Helper functions

// testFile represents a test file with its name and content
type testFile struct {
	name    string
	content string
}

// newTestFile creates a new test file with standard markdown content
func newTestFile(name, title, date string, tags, categories []string) testFile {
	var sb strings.Builder

	// Front matter
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("title: %s\n", title))
	sb.WriteString(fmt.Sprintf("date: %s\n", date))

	if len(tags) > 0 {
		sb.WriteString(fmt.Sprintf("tags: [%s]\n", strings.Join(tags, ", ")))
	}
	if len(categories) > 0 {
		sb.WriteString(fmt.Sprintf("categories: [%s]\n", strings.Join(categories, ", ")))
	}
	sb.WriteString("---\n")

	// Default content
	sb.WriteString(fmt.Sprintf("# %s\nThis is a test post", title))

	return testFile{name: name, content: sb.String()}
}

// createTestEnvironment creates temporary directories and files for testing
func createTestEnvironment(t testing.TB, files []testFile) (string, string) {
	t.Helper()
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	for _, file := range files {
		dir := filepath.Dir(filepath.Join(srcDir, file.name))
		err := os.MkdirAll(dir, 0755)
		require.NoError(t, err, "Failed to create directory: %s", dir)

		err = os.WriteFile(filepath.Join(srcDir, file.name), []byte(file.content), 0644)
		require.NoError(t, err, "Failed to create test file: %s", file.name)
	}

	return srcDir, dstDir
}

// verifyConvertedFile checks if a file was properly converted
func verifyConvertedFile(t *testing.T, dir string, file testFile) {
	t.Helper()
	verifyFileContent(t, dir, file.name, "This is a test post")
}

// verifyFileContent checks if a file contains the expected content
func verifyFileContent(t *testing.T, dir, name, expectedContent string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, name))
	require.NoError(t, err, "Failed to read converted file %s", name)

	assert.Equal(t, 2, strings.Count(string(content), "---"),
		"Expected 2 '---' separators in %s", name)
	assert.Contains(t, string(content), expectedContent,
		"Converted file %s does not contain expected content", name)
}
