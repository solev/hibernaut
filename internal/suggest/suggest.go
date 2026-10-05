// Package suggest picks the name a mistyped word most likely meant, for "Did you mean app?".
package suggest

import "strings"

// Closest returns the one name in names that word is closest to, if any. A name counts when
// word is within one edit of it, or two for names longer than four letters, where an edit adds,
// removes or changes a letter or swaps two neighbouring ones. If no name is that close, the one
// name that starts with word counts. Two names equally close give no suggestion.
func Closest(word string, names []string) (string, bool) {
	if word == "" {
		return "", false
	}
	w := []rune(word)
	best, bestDistance, tie := "", -1, false
	for _, name := range names {
		n := []rune(name)
		limit := 2
		if len(n) <= 4 {
			limit = 1
		}
		if abs(len(w)-len(n)) > limit {
			continue
		}
		d := distance(w, n)
		switch {
		case d > limit:
		case bestDistance < 0 || d < bestDistance:
			best, bestDistance, tie = name, d, false
		case d == bestDistance && name != best:
			tie = true
		}
	}
	switch {
	case tie:
		return "", false
	case bestDistance >= 0:
		return best, true
	}
	return onlyPrefixed(word, names)
}

func onlyPrefixed(word string, names []string) (string, bool) {
	found := ""
	for _, name := range names {
		if !strings.HasPrefix(name, word) || name == found {
			continue
		}
		if found != "" {
			return "", false
		}
		found = name
	}
	return found, found != ""
}

func distance(a, b []rune) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}

func abs(n int) int {
	return max(n, -n)
}
