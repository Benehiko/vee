package tui

import (
	"os"
	"path/filepath"
	"strings"
)

// PathSuggestions returns filesystem directory completions for prefix.
func PathSuggestions(prefix string) []string {
	dir, partial := filepath.Split(prefix)
	if dir == "" {
		dir = "."
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var completions []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, partial) {
			continue
		}
		full := filepath.Join(dir, name)
		completions = append(completions, full+"/")
	}
	return completions
}

func pathSuggestions(prefix string) []string { return PathSuggestions(prefix) }
