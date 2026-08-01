package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestProductionDiagnosticStaticGuardrails(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	forbiddenSlog := map[string]bool{
		"Default": true, "SetDefault": true,
		"Debug": true, "Info": true, "Warn": true, "Error": true, "Log": true, "LogAttrs": true,
	}
	isDiagnosticFile := func(path, name string) bool {
		return strings.HasSuffix(filepath.ToSlash(path), "/internal/diagnostic/"+name)
	}
	forbiddenMetadata := map[string]bool{"phase": true, "operation": true}
	metadataKey := func(expression ast.Expr) (string, bool) {
		literal, ok := expression.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return "", false
		}
		key, err := strconv.Unquote(literal.Value)
		return key, err == nil
	}
	checkMetadataKey := func(path string, expression ast.Expr) {
		key, ok := metadataKey(expression)
		if !ok {
			return
		}
		if forbiddenMetadata[key] {
			t.Errorf("forbidden diagnostic metadata key %q in %s", key, path)
		}
		allowedFiles := map[string][]string{
			"kind":      {"context.go", "stream.go"},
			"stream":    {"stream.go"},
			"component": {"context.go"},
			"error":     {"error.go"},
		}
		if allowed, reserved := allowedFiles[key]; reserved {
			approved := false
			for _, name := range allowed {
				approved = approved || isDiagnosticFile(path, name)
			}
			if !approved {
				t.Errorf("reserved metadata key %q at an unapproved record site in %s", key, path)
			}
		}
	}
	checkProtectedValue := func(path string, keyExpression, value ast.Expr) {
		checkMetadataKey(path, keyExpression)
		key, ok := metadataKey(keyExpression)
		if !ok {
			return
		}
		required := map[string]string{
			"error":   "SafeError",
			"argv":    "DiagnosticArgv",
			"summary": "Summarize",
		}[key]
		if required == "" {
			return
		}
		call, ok := value.(*ast.CallExpr)
		if !ok {
			t.Errorf("protected diagnostic field %q is not built by %s in %s", key, required, path)
			return
		}
		constructor := ""
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			constructor = fun.Name
		case *ast.SelectorExpr:
			constructor = fun.Sel.Name
		}
		if constructor != required {
			t.Errorf("protected diagnostic field %q is not built by %s in %s", key, required, path)
		}
	}

	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != repoRoot && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			return nil
		}
		slogNames := map[string]bool{}
		osNames := map[string]bool{}
		for _, imported := range file.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil || (importPath != "log/slog" && importPath != "os") {
				continue
			}
			name := filepath.Base(importPath)
			if imported.Name != nil {
				name = imported.Name.Name
			}
			if name == "." {
				t.Errorf("production dot import of %s in %s", importPath, path)
				continue
			}
			if importPath == "log/slog" {
				slogNames[name] = true
			} else {
				osNames[name] = true
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if selector, ok := node.(*ast.SelectorExpr); ok {
				if ident, ok := selector.X.(*ast.Ident); ok && osNames[ident.Name] &&
					selector.Sel.Name == "Stderr" && filepath.Base(path) != "main.go" {
					t.Errorf("production os.Stderr bypass outside main in %s", path)
				}
				return true
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if constructor, ok := call.Fun.(*ast.Ident); ok {
				if isDiagnosticFile(path, "error.go") && constructor.Name == "Value" && len(call.Args) > 1 {
					checkProtectedValue(path, call.Args[0], call.Args[1])
				}
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := selector.X.(*ast.Ident)
			if ok && slogNames[ident.Name] && forbiddenSlog[selector.Sel.Name] {
				t.Errorf("production package-level slog call in %s: slog.%s", path, selector.Sel.Name)
			}
			if ok && slogNames[ident.Name] && selector.Sel.Name == "Any" {
				t.Errorf("production raw slog.Any call in %s; use a reviewed typed carrier", path)
			}
			// Attribute constructors and logger key/value APIs put metadata keys
			// in different positions. Check all of them, not merely arg zero.
			switch selector.Sel.Name {
			case "String", "Bool", "Int", "Int64", "Uint64", "Float64", "Duration", "Time", "Any", "Group", "Value":
				if len(call.Args) > 0 {
					checkMetadataKey(path, call.Args[0])
				}
				if len(call.Args) > 1 {
					checkProtectedValue(path, call.Args[0], call.Args[1])
				}
			case "New", "NewTextHandler", "NewJSONHandler":
				approved := isDiagnosticFile(path, "context.go") || isDiagnosticFile(path, "stream.go")
				if ok && slogNames[ident.Name] && !approved {
					t.Errorf("production slog construction outside diagnostic stream in %s", path)
				}
			case "Debug", "Info", "Warn", "Error", "With":
				start := 1
				if selector.Sel.Name == "With" {
					start = 0
				}
				for i := start; i < len(call.Args); i += 2 {
					if i+1 < len(call.Args) {
						checkProtectedValue(path, call.Args[i], call.Args[i+1])
					} else {
						checkMetadataKey(path, call.Args[i])
					}
				}
			case "Log":
				for i := 3; i < len(call.Args); i += 2 {
					if i+1 < len(call.Args) {
						checkProtectedValue(path, call.Args[i], call.Args[i+1])
					} else {
						checkMetadataKey(path, call.Args[i])
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
