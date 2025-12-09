package main

import (
	"fmt"
	"go/ast"
	"strings"
)

// TypeInfo holds analyzed type information
type TypeInfo struct {
	Name   string
	Fields []*FieldInfo
}

// FieldInfo holds analyzed field information
type FieldInfo struct {
	Name       string
	TypeExpr   ast.Expr  // Original AST expression
	TypeStr    string    // String representation
	Kind       FieldKind // Categorized kind
	ElemKind   FieldKind // Element kind for slices/maps/pointers
	KeyKind    FieldKind // Key kind for maps
	ElemType   string    // Element type string
	KeyType    string    // Key type for maps
	HasClone   bool      // Whether the type has Clone() method
	IsExported bool      // Whether the field is exported
	Embedded   bool      // Whether this is an embedded field
	ArrayLen   string    // Array length for fixed arrays
	Warning    string    // Any warnings for this field
}

// FieldKind categorizes field types
type FieldKind int

const (
	KindPrimitive FieldKind = iota
	KindString
	KindSlice
	KindArray
	KindMap
	KindPointer
	KindStruct
	KindInterface
	KindChan
	KindFunc
	KindTime
	KindUnknown
)

func (k FieldKind) String() string {
	switch k {
	case KindPrimitive:
		return "primitive"
	case KindString:
		return "string"
	case KindSlice:
		return "slice"
	case KindArray:
		return "array"
	case KindMap:
		return "map"
	case KindPointer:
		return "pointer"
	case KindStruct:
		return "struct"
	case KindInterface:
		return "interface"
	case KindChan:
		return "chan"
	case KindFunc:
		return "func"
	case KindTime:
		return "time"
	default:
		return "unknown"
	}
}

// Analyzer analyzes struct types
type Analyzer struct {
	pkg         *Package
	cloneMethod string
}

// NewAnalyzer creates a new type analyzer
func NewAnalyzer(pkg *Package, cloneMethod string) *Analyzer {
	return &Analyzer{
		pkg:         pkg,
		cloneMethod: cloneMethod,
	}
}

// Analyze analyzes a named type
func (a *Analyzer) Analyze(typeName string) (*TypeInfo, error) {
	structType, ok := a.pkg.Structs[typeName]
	if !ok {
		return nil, fmt.Errorf("type %s not found", typeName)
	}

	info := &TypeInfo{
		Name:   typeName,
		Fields: make([]*FieldInfo, 0),
	}

	for _, field := range structType.Fields.List {
		fieldInfo := a.analyzeField(field)

		if len(field.Names) == 0 {
			// Embedded field
			fieldInfo.Embedded = true
			fieldInfo.Name = fieldInfo.TypeStr
			// Extract just the type name for embedded fields
			if idx := strings.LastIndex(fieldInfo.TypeStr, "."); idx >= 0 {
				fieldInfo.Name = fieldInfo.TypeStr[idx+1:]
			}
			// Remove pointer prefix for name
			fieldInfo.Name = strings.TrimPrefix(fieldInfo.Name, "*")
			info.Fields = append(info.Fields, fieldInfo)
		} else {
			// Named field(s)
			for _, name := range field.Names {
				f := *fieldInfo // Copy
				f.Name = name.Name
				f.IsExported = ast.IsExported(name.Name)
				info.Fields = append(info.Fields, &f)
			}
		}
	}

	return info, nil
}

// analyzeField analyzes a single field
func (a *Analyzer) analyzeField(field *ast.Field) *FieldInfo {
	info := &FieldInfo{
		TypeExpr:   field.Type,
		TypeStr:    exprToString(field.Type),
		IsExported: true,
	}

	a.categorizeType(info, field.Type)

	return info
}

// categorizeType determines the kind of a type expression
func (a *Analyzer) categorizeType(info *FieldInfo, expr ast.Expr) {
	switch t := expr.(type) {
	case *ast.Ident:
		info.Kind = a.identKind(t.Name)
		if info.Kind == KindStruct {
			info.HasClone = a.pkg.HasCloneMethod(t.Name, a.cloneMethod)
		}

	case *ast.SelectorExpr:
		// Qualified identifier like time.Time or pkg.Type
		typeStr := exprToString(t)
		if typeStr == "time.Time" || typeStr == "time.Duration" {
			info.Kind = KindTime
		} else {
			info.Kind = KindStruct
			// Can't easily check for Clone method on external types
		}

	case *ast.StarExpr:
		info.Kind = KindPointer
		info.ElemType = exprToString(t.X)
		info.ElemKind = a.exprKind(t.X)
		if info.ElemKind == KindStruct {
			if ident, ok := t.X.(*ast.Ident); ok {
				info.HasClone = a.pkg.HasCloneMethod(ident.Name, a.cloneMethod)
			}
		}

	case *ast.ArrayType:
		if t.Len == nil {
			// Slice
			info.Kind = KindSlice
		} else {
			// Fixed array
			info.Kind = KindArray
			info.ArrayLen = exprToString(t.Len)
		}
		info.ElemType = exprToString(t.Elt)
		info.ElemKind = a.exprKind(t.Elt)
		if info.ElemKind == KindStruct {
			if ident, ok := t.Elt.(*ast.Ident); ok {
				info.HasClone = a.pkg.HasCloneMethod(ident.Name, a.cloneMethod)
			}
		}
		// Check for pointer to struct
		if star, ok := t.Elt.(*ast.StarExpr); ok {
			if ident, ok := star.X.(*ast.Ident); ok {
				info.HasClone = a.pkg.HasCloneMethod(ident.Name, a.cloneMethod)
			}
		}

	case *ast.MapType:
		info.Kind = KindMap
		info.KeyType = exprToString(t.Key)
		info.KeyKind = a.exprKind(t.Key)
		info.ElemType = exprToString(t.Value)
		info.ElemKind = a.exprKind(t.Value)
		if info.ElemKind == KindStruct {
			if ident, ok := t.Value.(*ast.Ident); ok {
				info.HasClone = a.pkg.HasCloneMethod(ident.Name, a.cloneMethod)
			}
		}

	case *ast.InterfaceType:
		info.Kind = KindInterface
		info.Warning = "interface type - shallow copy only"

	case *ast.ChanType:
		info.Kind = KindChan
		info.Warning = "channel type - not cloned"

	case *ast.FuncType:
		info.Kind = KindFunc
		info.Warning = "function type - not cloned"

	default:
		info.Kind = KindUnknown
		info.Warning = "unknown type - shallow copy"
	}
}

// identKind returns the kind for an identifier
func (a *Analyzer) identKind(name string) FieldKind {
	switch name {
	case "bool":
		return KindPrimitive
	case "string":
		return KindString
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64",
		"complex64", "complex128",
		"byte", "rune":
		return KindPrimitive
	case "error":
		return KindInterface
	case "any":
		return KindInterface
	default:
		// Check if it's a struct in our package
		if _, ok := a.pkg.Structs[name]; ok {
			return KindStruct
		}
		// Assume it's a type alias or external type
		return KindStruct
	}
}

// exprKind returns the kind for an expression
func (a *Analyzer) exprKind(expr ast.Expr) FieldKind {
	switch t := expr.(type) {
	case *ast.Ident:
		return a.identKind(t.Name)
	case *ast.SelectorExpr:
		typeStr := exprToString(t)
		if typeStr == "time.Time" || typeStr == "time.Duration" {
			return KindTime
		}
		return KindStruct
	case *ast.StarExpr:
		return KindPointer
	case *ast.ArrayType:
		if t.Len == nil {
			return KindSlice
		}
		return KindArray
	case *ast.MapType:
		return KindMap
	case *ast.InterfaceType:
		return KindInterface
	case *ast.ChanType:
		return KindChan
	case *ast.FuncType:
		return KindFunc
	default:
		return KindUnknown
	}
}

// exprToString converts an AST expression to its string representation
func exprToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return exprToString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + exprToString(t.X)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + exprToString(t.Elt)
		}
		return "[" + exprToString(t.Len) + "]" + exprToString(t.Elt)
	case *ast.MapType:
		return "map[" + exprToString(t.Key) + "]" + exprToString(t.Value)
	case *ast.InterfaceType:
		if len(t.Methods.List) == 0 {
			return "any"
		}
		return "interface{...}"
	case *ast.ChanType:
		switch t.Dir {
		case ast.SEND:
			return "chan<- " + exprToString(t.Value)
		case ast.RECV:
			return "<-chan " + exprToString(t.Value)
		default:
			return "chan " + exprToString(t.Value)
		}
	case *ast.FuncType:
		return "func(...)"
	case *ast.BasicLit:
		return t.Value
	case *ast.Ellipsis:
		return "..." + exprToString(t.Elt)
	case *ast.IndexExpr:
		// Generic type like Container[T]
		return exprToString(t.X) + "[" + exprToString(t.Index) + "]"
	case *ast.IndexListExpr:
		// Generic type with multiple params
		var indices []string
		for _, idx := range t.Indices {
			indices = append(indices, exprToString(idx))
		}
		return exprToString(t.X) + "[" + strings.Join(indices, ", ") + "]"
	default:
		return fmt.Sprintf("%T", expr)
	}
}
