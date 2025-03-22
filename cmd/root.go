package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pplmx/h2h/internal"
	"github.com/spf13/cobra"
)

var (
	srcDir  string
	dstDir  string
	config  *internal.Config
	rootCmd *cobra.Command
)

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	config = internal.NewDefaultConfig()
	initRootCmd()
	initFlags()
}

func initRootCmd() {
	rootCmd = &cobra.Command{
		Use:   "h2h",
		Short: "Convert between Hexo and Hugo FrontMatter",
		Long: `h2h is a tool to convert between Hexo and Hugo FrontMatter.
It can be used to migrate a Hexo blog to Hugo or a Hugo blog to Hexo.
The tool processes Markdown files with either Hexo or Hugo FrontMatter and converts them to the other format.
Converted files are written to the specified destination directory.

By default, it converts from Hexo to Hugo format using YAML.`,
		RunE: runConversion,
	}
}

func initFlags() {
	flags := rootCmd.Flags()
	flags.StringVar(&srcDir, "src", "", "source directory containing Markdown files to convert (required)")
	flags.StringVar(&dstDir, "dst", "", "destination directory to write converted Markdown files (required)")
	flags.StringVar((*string)(&config.SourceFormat), "source-format", string(config.SourceFormat), "source FrontMatter format (yaml or toml)")
	flags.StringVar((*string)(&config.TargetFormat), "target-format", string(config.TargetFormat), "target FrontMatter format (yaml or toml)")
	flags.StringVar(&config.FileExtension, "file-extension", config.FileExtension, "file extension for Markdown files")
	flags.IntVar(&config.MaxConcurrency, "max-concurrency", config.MaxConcurrency, "maximum number of concurrent file conversions")
	flags.StringVar((*string)(&config.ConversionDirection), "direction", string(config.ConversionDirection), "conversion direction (hexo2hugo or hugo2hexo)")

	cobra.CheckErr(rootCmd.MarkFlagRequired("src"))
	cobra.CheckErr(rootCmd.MarkFlagRequired("dst"))
}

func runConversion(cmd *cobra.Command, args []string) error {
	fmt.Printf("Starting conversion from [%s] to [%s] format, direction: %s, output will be written to [%s]\n",
		config.SourceFormat, config.TargetFormat, config.ConversionDirection, dstDir)

	srcDirAbs, err := filepath.Abs(srcDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for source directory: %w", err)
	}

	dstDirAbs, err := filepath.Abs(dstDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for destination directory: %w", err)
	}

	if err := internal.ConvertPosts(srcDirAbs, dstDirAbs, config); err != nil {
		return fmt.Errorf("conversion failed: %w", err)
	}

	fmt.Println("Conversion completed successfully")
	return nil
}
