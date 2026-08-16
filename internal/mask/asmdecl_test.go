package mask

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var textSymbolRe = regexp.MustCompile(`(?m)^TEXT\s+·([A-Za-z0-9_]+)\(SB\)`)

// TestNoescapeDeclsHaveTextSymbols checks that every body-less Go function
// declaration anywhere under internal/mask has a matching TEXT symbol in a .s file
// in the same directory.
//
// It walks the whole package tree rather than one assembly directory so that a
// second kernel package - blurkernel's and the mask package's own live side by
// side - cannot be added without inheriting the check.
//
// Build tags are deliberately ignored: the point is to catch a declaration
// whose implementation was renamed or deleted, which on the host architecture
// would be a link error but on every other architecture would go unnoticed
// until someone cross-compiles.
func TestNoescapeDeclsHaveTextSymbols(t *testing.T) {
	root := "."

	dirs := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".s") {
			dirs[filepath.Dir(path)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	checked := 0
	for dir := range dirs {
		symbols := textSymbolsIn(t, dir)
		for _, name := range bodylessFuncsIn(t, dir) {
			if !symbols[name] {
				t.Errorf("%s: func %s is declared without a body but no .s file defines TEXT ·%s(SB)",
					dir, name, name)
			}
			checked++
		}
	}

	if checked == 0 {
		t.Fatal("no assembly declarations found; this test is not checking anything")
	}
}

func textSymbolsIn(t *testing.T, dir string) map[string]bool {
	t.Helper()

	symbols := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".s") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", entry.Name(), err)
		}
		for _, m := range textSymbolRe.FindAllStringSubmatch(string(src), -1) {
			symbols[m[1]] = true
		}
	}
	return symbols
}

func bodylessFuncsIn(t *testing.T, dir string) []string {
	t.Helper()

	var names []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Body == nil && fn.Recv == nil {
				names = append(names, fn.Name.Name)
			}
		}
	}
	return names
}
