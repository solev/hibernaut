package suggest

import (
	"strings"
	"testing"
)

var commands = []string{
	"init", "new", "ls", "rm", "pause", "fork", "ssh", "exec", "image", "secret", "connect",
	"disconnect", "status", "log", "completion", "bridge", "version", "help",
}

func TestClosest(t *testing.T) {
	for _, tc := range []struct {
		word  string
		names []string
		want  string
	}{
		{"nwe", commands, "new"},
		{"staus", commands, "status"},
		{"stauts", commands, "status"},
		{"imgae", commands, "image"},
		{"secert", commands, "secret"},
		{"lss", commands, "ls"},
		{"fokr", commands, "fork"},
		{"vresoin", commands, "version"},
		{"conect", commands, "connect"},
		{"new", commands, "new"},
		{"comp", commands, "completion"},
		{"dis", commands, "disconnect"},
		{"ap", []string{"app", "web"}, "app"},
		{"ap", []string{"app", "apt"}, ""},
		{"aps", []string{"app", "apt"}, ""},
		{"co", commands, ""},
		{"fk", commands, ""},
		{"xyz", commands, ""},
		{"vrseoin", commands, ""},
		{"", commands, ""},
		{"new", nil, ""},
		{"wbe-1", []string{"web-1", "web-2"}, "web-1"},
		{"web-3", []string{"web-1", "web-2"}, ""},
		{"ünew", []string{"new", "news-feed"}, "new"},
		{strings.Repeat("a", 10000), []string{"aaaa"}, ""},
	} {
		got, ok := Closest(tc.word, tc.names)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("Closest(%q, %q) = %q, %v; want %q", tc.word, tc.names, got, ok, tc.want)
		}
	}
}

func TestClosestCountsASwapAsOneEdit(t *testing.T) {
	for word, name := range map[string]string{"tset": "test", "ba": "ab", "abdc": "abcd"} {
		if got, ok := Closest(word, []string{name, "zzzzzzzz"}); !ok || got != name {
			t.Errorf("Closest(%q) = %q, %v; want %q", word, got, ok, name)
		}
	}
}

func TestClosestAllowsTwoEditsOnlyForNamesLongerThanFourLetters(t *testing.T) {
	for _, tc := range []struct {
		word, name string
		want       bool
	}{
		{"abxd", "abcd", true},
		{"abxy", "abcd", false},
		{"abcdxy", "abcd", false},
		{"abxye", "abcde", true},
		{"abcdexy", "abcde", true},
		{"axyze", "abcde", false},
	} {
		got, ok := Closest(tc.word, []string{tc.name})
		if ok != tc.want || ok && got != tc.name {
			t.Errorf("Closest(%q, [%q]) = %q, %v; want a suggestion: %v", tc.word, tc.name, got, ok, tc.want)
		}
	}
}
