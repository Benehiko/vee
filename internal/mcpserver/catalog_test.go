package mcpserver

import (
	"slices"
	"testing"

	"github.com/Benehiko/vee/internal/vm/build"
)

// TestTemplateCatalogMatchesBuild keeps template_list's catalog in lockstep
// with the templates build.Build actually dispatches (build.KnownTemplates,
// itself guarded against the dispatch switch by the build package's tests).
func TestTemplateCatalogMatchesBuild(t *testing.T) {
	var catalog []string
	for _, tmpl := range templateCatalog {
		catalog = append(catalog, tmpl.Name)
	}
	known := slices.Clone(build.KnownTemplates)
	slices.Sort(catalog)
	slices.Sort(known)
	if !slices.Equal(catalog, known) {
		t.Errorf("templateCatalog and build.KnownTemplates disagree:\n  catalog: %v\n  known:   %v\nDescribe new templates in templateCatalog.", catalog, known)
	}
}
