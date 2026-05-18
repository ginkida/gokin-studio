package studio

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// file_suggest.go (iter 520+) -- recursive file lister for the inline
// @path autocomplete popup. The chat input pops up matching files when
// users type @<chars>; we need a snapshot of every file in the project
// so the frontend can fuzzy-match locally without an RPC per keystroke.
//
// Excludes common noise directories that aren't meaningful @-targets
// (.git, build output, dependency caches, etc.) and caps at 5000 entries
// so a monorepo doesn't blow up the bridge payload.

const fileSuggestMaxEntries = 5000

// noiseDirNames is checked against each directory's basename. Hits skip
// the directory entirely. Conservative — we'd rather miss a few obscure
// files than drown the user in node_modules/.
var noiseDirNames = map[string]bool{
	".git":          true,
	"node_modules":  true,
	"dist":          true,
	"build":         true,
	"target":        true,
	"vendor":        true,
	".next":         true,
	".nuxt":         true,
	".venv":         true,
	"venv":          true,
	"__pycache__":   true,
	".pytest_cache": true,
	".cache":        true,
	".idea":         true,
	".vscode":       true,
	"out":           true,
	"coverage":      true,
	".gokin":        true, // our own metadata dir
}

// ListProjectFiles walks the project directory recursively and returns
// relative paths to every regular file, alphabetically sorted. Designed
// for the @path autocomplete: the frontend caches the result per project
// and filters locally.
//
// Excludes hidden directories (basename starts with ".") AND the explicit
// noiseDirNames list. Hidden FILES at the project root are kept (e.g.
// .gitignore, .env.example) because users do want to reference those.
//
// Returns up to fileSuggestMaxEntries paths. If the cap is reached, walking
// stops early — the user sees a partial result rather than a delayed
// complete one.
func (s *Studio) ListProjectFiles(projectID string) ([]string, error) {
	if projectID == "" {
		return nil, fmt.Errorf("projectID cannot be empty")
	}
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	root := p.Directory
	if root == "" {
		return []string{}, nil
	}

	out := make([]string, 0, 256)
	stop := false
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if stop {
			return filepath.SkipDir
		}
		if err != nil {
			// A permission-denied or stat error on one entry shouldn't fail
			// the whole walk. Skip the offending path and continue — partial
			// results are more useful than no results.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == root {
			return nil // don't emit the root itself
		}
		name := d.Name()
		if d.IsDir() {
			// Skip noise directories AND hidden directories (except root).
			if noiseDirNames[name] || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		// Regular file: emit relative path.
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil // can't happen for paths under root, but handle defensively
		}
		// Normalize separators to forward slashes for cross-platform consistency
		// (the @path syntax in chat is always /-separated).
		rel = filepath.ToSlash(rel)
		out = append(out, rel)
		if len(out) >= fileSuggestMaxEntries {
			stop = true
			return filepath.SkipDir
		}
		return nil
	})
	if walkErr != nil {
		// Only surface walk errors that aren't from a SkipDir we issued.
		// At this level, walkErr is typically nil (errors are swallowed in
		// the per-entry callback above), so this branch mostly catches
		// catastrophic filesystem failures.
		return nil, walkErr
	}
	sort.Strings(out)
	return out, nil
}
