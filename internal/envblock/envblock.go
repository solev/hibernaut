// Package envblock renders and merges the managed secrets block in /etc/environment.
package envblock

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const (
	beginLine = "# BEGIN hibernaut secrets: hibd manages this block and replaces edits"
	endLine   = "# END hibernaut secrets"
)

var (
	nameRE  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	valueRE = regexp.MustCompile(`^[A-Za-z0-9._~+/=:-]+$`)
)

// Var is one placeholder environment variable in the managed block.
type Var struct {
	Name  string
	Value string
}

// Render returns the managed /etc/environment block for vars.
func Render(vars []Var) ([]byte, error) {
	sorted := slices.Clone(vars)
	seen := map[string]bool{}
	for _, v := range sorted {
		if !nameRE.MatchString(v.Name) {
			return nil, fmt.Errorf("bad environment variable name %q", v.Name)
		}
		if !valueRE.MatchString(v.Value) {
			return nil, fmt.Errorf("bad placeholder value for %s", v.Name)
		}
		if seen[v.Name] {
			return nil, fmt.Errorf("duplicate environment variable %s", v.Name)
		}
		seen[v.Name] = true
	}
	slices.SortFunc(sorted, func(a, b Var) int { return strings.Compare(a.Name, b.Name) })
	var out bytes.Buffer
	out.WriteString(beginLine + "\n")
	for _, v := range sorted {
		out.WriteString(v.Name)
		out.WriteByte('=')
		out.WriteString(v.Value)
		out.WriteByte('\n')
	}
	out.WriteString(endLine + "\n")
	return out.Bytes(), nil
}

// Merge replaces the managed block in file, or appends block if file has no block.
func Merge(file, block []byte) ([]byte, error) {
	found, err := findBlock(file)
	if err != nil {
		return nil, err
	}
	if found.ok {
		out := make([]byte, 0, len(file)-found.end+found.start+len(block))
		out = append(out, file[:found.start]...)
		out = append(out, block...)
		out = append(out, file[found.end:]...)
		return out, nil
	}
	if len(block) == 0 {
		return slices.Clone(file), nil
	}
	out := slices.Clone(file)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, block...)
	return out, nil
}

// Parse returns the variables from file's managed block.
func Parse(file []byte) ([]Var, error) {
	found, err := findBlock(file)
	if err != nil {
		return nil, err
	}
	if !found.ok {
		return nil, nil
	}
	var vars []Var
	for line := range eachLine(file[found.bodyStart:found.bodyEnd]) {
		if len(line.text) == 0 {
			continue
		}
		name, value, ok := strings.Cut(string(line.text), "=")
		if !ok {
			return nil, fmt.Errorf("bad line in managed block: %q", line.text)
		}
		if !nameRE.MatchString(name) {
			return nil, fmt.Errorf("bad environment variable name %q", name)
		}
		if !valueRE.MatchString(value) {
			return nil, fmt.Errorf("bad placeholder value for %s", name)
		}
		vars = append(vars, Var{Name: name, Value: value})
	}
	return vars, nil
}

type blockLocation struct {
	ok                 bool
	start, end         int
	bodyStart, bodyEnd int
}

type line struct {
	text       []byte
	start, end int
}

func findBlock(file []byte) (blockLocation, error) {
	var loc blockLocation
	for l := range eachLine(file) {
		switch string(l.text) {
		case beginLine:
			if loc.ok || loc.start != 0 || loc.bodyStart != 0 {
				return blockLocation{}, errors.New("more than one managed block")
			}
			loc.start = l.start
			loc.bodyStart = l.end
		case endLine:
			if loc.ok {
				return blockLocation{}, errors.New("more than one managed block")
			}
			if loc.bodyStart == 0 && loc.start == 0 {
				return blockLocation{}, errors.New("managed block end before begin")
			}
			loc.ok = true
			loc.bodyEnd = l.start
			loc.end = l.end
		}
	}
	if loc.bodyStart != 0 && !loc.ok {
		return blockLocation{}, errors.New("managed block begin without end")
	}
	return loc, nil
}

func eachLine(b []byte) func(func(line) bool) {
	return func(yield func(line) bool) {
		for start := 0; start < len(b); {
			end := start + bytes.IndexByte(b[start:], '\n')
			if end < start {
				if !yield(line{text: b[start:], start: start, end: len(b)}) {
					return
				}
				return
			}
			if !yield(line{text: b[start:end], start: start, end: end + 1}) {
				return
			}
			start = end + 1
		}
	}
}
