package build

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"
)

// TestKnownTemplatesMatchDispatch guards KnownTemplates against drifting from
// the template switch in configFromTemplate. A template added to the switch
// but not the list would be invisible to every surface that enumerates
// templates (CLI help, the MCP server's template_list); the reverse would
// advertise a template that silently builds the default config.
func TestKnownTemplatesMatchDispatch(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "build.go", nil, 0)
	if err != nil {
		t.Fatalf("parse build.go: %v", err)
	}

	var dispatched []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "configFromTemplate" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range cc.List {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				dispatched = append(dispatched, lit.Value[1:len(lit.Value)-1])
			}
			return true
		})
	}
	if len(dispatched) == 0 {
		t.Fatal("found no case labels in configFromTemplate — was the switch moved or renamed?")
	}

	want := slices.Clone(KnownTemplates)
	slices.Sort(want)
	slices.Sort(dispatched)
	if !slices.Equal(want, dispatched) {
		t.Errorf("KnownTemplates and configFromTemplate's switch disagree:\n  KnownTemplates: %v\n  switch cases:   %v\nAdd new templates to both.", want, dispatched)
	}
}
