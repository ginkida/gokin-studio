package tools

import (
	"math"
	"sort"
	"strings"
)

// sortFileMatchesByRelevance ranks grep results in-place so the most
// likely-relevant file hits appear first. Stable ordering is preserved
// for ties (path-asc tiebreaker) so re-running the same search yields
// the same top-N.
//
// Ported from gokin internal/tools/file_relevance.go.
func sortFileMatchesByRelevance(results []fileMatch) {
	sort.SliceStable(results, func(i, j int) bool {
		si := fileRelevanceScore(results[i].path, len(results[i].matches))
		sj := fileRelevanceScore(results[j].path, len(results[j].matches))
		if si != sj {
			return si > sj
		}
		return results[i].path < results[j].path
	})
}

// fileRelevanceScore returns a relevance score for a path in a grep result.
// Higher = better. Scoring factors:
//   - log2(matchCount+1): diminishing returns on raw count (100-hit vendor
//     can't drown a 5-hit source file)
//   - path-depth penalty: -0.3 per slash, capped at 6 levels
//   - non-test bonus (+2.5): definition sites outrank usage sites
//   - non-vendor bonus (+5.0): first-party source always beats vendored deps
//   - non-generated bonus (+0.7): proto/gen/minified files rank last
//
// Pinned: 2-hit source file outranks 12-hit vendor file outranks 8-hit test.
func fileRelevanceScore(path string, matchCount int) float64 {
	if matchCount < 0 {
		matchCount = 0
	}
	score := math.Log2(float64(matchCount + 1))

	depth := strings.Count(path, "/")
	if depth > 6 {
		depth = 6
	}
	score -= float64(depth) * 0.3

	if !isTestPath(path) {
		score += 2.5
	}
	if !isVendorPath(path) {
		score += 5.0
	}
	if !isGeneratedPath(path) {
		score += 0.7
	}

	return score
}

// isTestPath flags test, spec, and fixture files/directories.
func isTestPath(path string) bool {
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, "_test.go") ||
		strings.HasSuffix(lower, ".test.ts") ||
		strings.HasSuffix(lower, ".test.tsx") ||
		strings.HasSuffix(lower, ".test.js") ||
		strings.HasSuffix(lower, ".test.jsx") ||
		strings.HasSuffix(lower, ".spec.ts") ||
		strings.HasSuffix(lower, ".spec.js") ||
		strings.HasSuffix(lower, "_spec.rb") {
		return true
	}
	base := lower
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	if strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") {
		return true
	}
	if strings.HasSuffix(base, "_test.py") {
		return true
	}
	segments := strings.Split(lower, "/")
	for _, seg := range segments {
		switch seg {
		case "__tests__", "tests", "test", "testdata", "fixtures", "__fixtures__":
			return true
		}
	}
	return false
}

// isVendorPath flags vendored dependencies and build byproducts.
func isVendorPath(path string) bool {
	lower := strings.ToLower(path)
	markers := []string{
		"/vendor/", "/node_modules/", "/.git/",
		"/build/", "/dist/", "/target/",
		"/.venv/", "/venv/", "/__pycache__/",
		"/.next/", "/.nuxt/", "/.svelte-kit/",
	}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	prefixes := []string{
		"vendor/", "node_modules/", ".git/",
		"build/", "dist/", "target/",
		".venv/", "venv/", "__pycache__/",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// isGeneratedPath flags machine-produced files (proto, gen, minified).
func isGeneratedPath(path string) bool {
	lower := strings.ToLower(path)
	markers := []string{
		".pb.go", ".pb.ts", ".pb.py",
		"_gen.go", ".gen.go", ".gen.ts",
		"_generated.go", ".generated.ts",
		".min.js", ".min.css",
		".bundle.js",
	}
	for _, m := range markers {
		if strings.HasSuffix(lower, m) {
			return true
		}
	}
	return false
}
