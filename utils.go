package main

import (
	"sort"
	"strings"
)

// levenshteinDistance calculates the edit distance between two strings.
// Adjacent transpositions count as one edit (Damerau-Levenshtein), so the
// most common typo class — swapped letters like "prinltn" for "println" —
// ranks closest in "did you mean" suggestions.
func levenshteinDistance(s1, s2 string) int {
	if len(s1) == 0 {
		return len(s2)
	}
	if len(s2) == 0 {
		return len(s1)
	}

	// Create matrix
	matrix := make([][]int, len(s1)+1)
	for i := range matrix {
		matrix[i] = make([]int, len(s2)+1)
	}

	// Initialize first row and column
	for i := 0; i <= len(s1); i++ {
		matrix[i][0] = i
	}
	for j := 0; j <= len(s2); j++ {
		matrix[0][j] = j
	}

	// Fill matrix
	for i := 1; i <= len(s1); i++ {
		for j := 1; j <= len(s2); j++ {
			cost := 1
			if s1[i-1] == s2[j-1] {
				cost = 0
			}
			matrix[i][j] = min(
				matrix[i-1][j]+1, // deletion
				min(matrix[i][j-1]+1, // insertion
					matrix[i-1][j-1]+cost)) // substitution
			// Transposition of adjacent characters counts as a single edit.
			if i > 1 && j > 1 && s1[i-1] == s2[j-2] && s1[i-2] == s2[j-1] {
				matrix[i][j] = min(matrix[i][j], matrix[i-2][j-2]+1)
			}
		}
	}

	return matrix[len(s1)][len(s2)]
}

// findSimilarIdentifiers finds identifiers similar to the given name
func findSimilarIdentifiers(name string, availableVars map[string]int, maxSuggestions int) []string {
	type suggestion struct {
		name     string
		distance int
	}

	var suggestions []suggestion
	// Allow about one edit per three characters, so short names only match
	// near-identical ones.
	threshold := min(3, (len(name)+1)/3)

	for varName := range availableVars {
		dist := levenshteinDistance(name, varName)
		if dist <= threshold && dist > 0 && dist < len(varName) {
			suggestions = append(suggestions, suggestion{varName, dist})
		}
	}

	// Sort by distance (closest first)
	sort.Slice(suggestions, func(i, j int) bool {
		if suggestions[i].distance == suggestions[j].distance {
			return suggestions[i].name < suggestions[j].name
		}
		return suggestions[i].distance < suggestions[j].distance
	})

	// Return top suggestions
	result := make([]string, 0, maxSuggestions)
	for i := 0; i < len(suggestions) && i < maxSuggestions; i++ {
		result = append(result, suggestions[i].name)
	}
	return result
}

// deriveAliasFromSource extracts a suitable alias from an import source
// Examples:
// - "github.com/user/repo" -> "repo"
// - "github.com/user/repo@v1.0.0" -> "repo"
// - "sdl3" -> "sdl3"
// - "./mylib" -> "mylib"
func deriveAliasFromSource(source string) string {
	// Remove version suffix if present
	if idx := strings.Index(source, "@"); idx != -1 {
		source = source[:idx]
	}

	// Remove trailing slashes
	source = strings.TrimRight(source, "/\\")

	// Get the last component
	if lastSlash := strings.LastIndexAny(source, "/\\"); lastSlash != -1 {
		return source[lastSlash+1:]
	}

	// No slashes, use as-is
	return source
}
