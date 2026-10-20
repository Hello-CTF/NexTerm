package sharing

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// TestNoBlindSleepsInPackageTests is an AST ratchet over the sharing,
// runbook, and sshconfig test suites: waiting must happen on completion
// signals (channels, WaitGroups) or injected clocks, never on time.Sleep,
// which only passes under favorable scheduling and slows the suite down by
// its full duration on every run. Update this ratchet only for an
// intentional, reviewed waiting strategy.
func TestNoBlindSleepsInPackageTests(t *testing.T) {
	for _, dir := range []string{".", "../runbook", "../sshconfig", "../sshconfig/termiusdb"} {
		files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		if len(files) == 0 {
			t.Fatalf("no test files found in %s", dir)
		}
		for _, file := range files {
			fset := token.NewFileSet()
			parsed, err := parser.ParseFile(fset, file, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", file, err)
			}
			ast.Inspect(parsed, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "Sleep" {
					return true
				}
				pkg, ok := selector.X.(*ast.Ident)
				if !ok || pkg.Name != "time" {
					return true
				}
				position := fset.Position(call.Pos())
				t.Errorf("%s:%d: time.Sleep in a test; wait on a completion signal or an injected clock instead", position.Filename, position.Line)
				return true
			})
		}
	}
}
