package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"

	"golang.org/x/tools/go/packages"
)

// Package holds parsed package information
type Package struct {
	Name       string
	Dir        string
	Fset       *token.FileSet
	Files      []*ast.File
	TypesInfo  *types.Info
	TypesPkg   *types.Package
	Structs    map[string]*ast.StructType
	StructObjs map[string]*types.Struct
}

// parsePackage parses the Go package in the given directory
func parsePackage(dir string) (*Package, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedSyntax,
		Dir: absDir,
	}

	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return nil, fmt.Errorf("load package: %w", err)
	}

	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no packages found in %s", dir)
	}

	if len(pkgs[0].Errors) > 0 {
		// Try fallback to AST-only parsing if type checking fails
		return parsePackageAST(absDir)
	}

	p := pkgs[0]

	pkg := &Package{
		Name:       p.Name,
		Dir:        absDir,
		Fset:       p.Fset,
		Files:      p.Syntax,
		TypesInfo:  p.TypesInfo,
		TypesPkg:   p.Types,
		Structs:    make(map[string]*ast.StructType),
		StructObjs: make(map[string]*types.Struct),
	}

	// Extract struct definitions
	for _, file := range p.Syntax {
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}

			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}

				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}

				name := typeSpec.Name.Name
				pkg.Structs[name] = structType

				// Get types.Struct from type info
				if obj := p.Types.Scope().Lookup(name); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if st, ok := named.Underlying().(*types.Struct); ok {
							pkg.StructObjs[name] = st
						}
					}
				}
			}
		}
	}

	return pkg, nil
}

// parsePackageAST parses package using only AST (fallback when type checking fails)
func parsePackageAST(dir string) (*Package, error) {
	fset := token.NewFileSet()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var files []*ast.File
	var pkgName string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if filepath.Ext(name) != ".go" {
			continue
		}
		// Skip test files and generated files
		if len(name) > 8 && name[len(name)-8:] == "_test.go" {
			continue
		}
		if len(name) > 12 && name[len(name)-12:] == "_clone_gen.go" {
			continue
		}

		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			continue
		}

		if pkgName == "" {
			pkgName = file.Name.Name
		}
		files = append(files, file)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no Go files found in %s", dir)
	}

	pkg := &Package{
		Name:       pkgName,
		Dir:        dir,
		Fset:       fset,
		Files:      files,
		Structs:    make(map[string]*ast.StructType),
		StructObjs: make(map[string]*types.Struct),
	}

	// Extract struct definitions from AST only
	for _, file := range files {
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}

			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}

				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}

				pkg.Structs[typeSpec.Name.Name] = structType
			}
		}
	}

	return pkg, nil
}

// HasCloneMethod checks if a type has a Clone() method
func (p *Package) HasCloneMethod(typeName string, methodName string) bool {
	if p.TypesPkg == nil {
		return false
	}

	obj := p.TypesPkg.Scope().Lookup(typeName)
	if obj == nil {
		return false
	}

	named, ok := obj.Type().(*types.Named)
	if !ok {
		return false
	}

	for i := 0; i < named.NumMethods(); i++ {
		if named.Method(i).Name() == methodName {
			return true
		}
	}

	// Check pointer receiver methods too
	ptr := types.NewPointer(named)
	mset := types.NewMethodSet(ptr)
	for i := 0; i < mset.Len(); i++ {
		if mset.At(i).Obj().Name() == methodName {
			return true
		}
	}

	return false
}
