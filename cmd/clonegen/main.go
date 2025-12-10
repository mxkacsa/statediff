// clonegen generates Clone() methods for Go structs.
//
// Usage:
//
//	//go:generate clonegen -type=GameState,Player
//
//	go generate ./...
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

var (
	typeNames       = flag.String("type", "", "comma-separated list of type names (required)")
	output          = flag.String("output", "", "output file name (default: {package}_clone_gen.go)")
	pointerReceiver = flag.Bool("pointer-receiver", false, "use pointer receiver: func (src *T) Clone() T")
	skipFields      = flag.String("skip-fields", "", "comma-separated fields to skip (shallow copy)")
	cloneMethod     = flag.String("clone-method", "Clone", "name of clone method to look for on nested types")
	verbose         = flag.Bool("verbose", false, "print detailed generation info")
)

func main() {
	flag.Parse()

	if *typeNames == "" {
		fmt.Fprintln(os.Stderr, "error: -type flag is required")
		flag.Usage()
		os.Exit(1)
	}

	types := strings.Split(*typeNames, ",")
	for i := range types {
		types[i] = strings.TrimSpace(types[i])
	}

	skipFieldsList := []string{}
	if *skipFields != "" {
		skipFieldsList = strings.Split(*skipFields, ",")
		for i := range skipFieldsList {
			skipFieldsList[i] = strings.TrimSpace(skipFieldsList[i])
		}
	}

	cfg := Config{
		Types:           types,
		Output:          *output,
		PointerReceiver: *pointerReceiver,
		SkipFields:      skipFieldsList,
		CloneMethod:     *cloneMethod,
		Verbose:         *verbose,
	}

	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// Config holds generation configuration
type Config struct {
	Types           []string
	Output          string
	PointerReceiver bool
	SkipFields      []string
	CloneMethod     string
	Verbose         bool
}

func run(cfg Config) error {
	// Get the directory to process (current directory or from GOFILE env)
	dir := "."
	if gofile := os.Getenv("GOFILE"); gofile != "" {
		if cfg.Verbose {
			fmt.Printf("Processing file: %s\n", gofile)
		}
	}

	// Parse the package
	pkg, err := parsePackage(dir)
	if err != nil {
		return fmt.Errorf("parse package: %w", err)
	}

	if cfg.Verbose {
		fmt.Printf("Package: %s\n", pkg.Name)
		fmt.Printf("Types to generate: %v\n", cfg.Types)
	}

	// Analyze types
	analyzer := NewAnalyzer(pkg, cfg.CloneMethod)
	typeInfos := make([]*TypeInfo, 0, len(cfg.Types))

	for _, typeName := range cfg.Types {
		info, err := analyzer.Analyze(typeName)
		if err != nil {
			return fmt.Errorf("analyze type %s: %w", typeName, err)
		}
		typeInfos = append(typeInfos, info)

		if cfg.Verbose {
			fmt.Printf("Analyzed %s: %d fields\n", typeName, len(info.Fields))
		}
	}

	// Generate code
	gen := NewGenerator(pkg.Name, cfg.PointerReceiver, cfg.SkipFields, pkg.Imports)
	code, err := gen.Generate(typeInfos)
	if err != nil {
		return fmt.Errorf("generate: %w", err)
	}

	// Determine output filename
	outputFile := cfg.Output
	if outputFile == "" {
		outputFile = fmt.Sprintf("%s_clone_gen.go", strings.ToLower(pkg.Name))
	}

	// Write output
	if err := os.WriteFile(outputFile, code, 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	if cfg.Verbose {
		fmt.Printf("Generated: %s\n", outputFile)
	}

	return nil
}
