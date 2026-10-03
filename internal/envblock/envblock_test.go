package envblock_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/envblock"
)

func TestRender(t *testing.T) {
	got, err := envblock.Render([]envblock.Var{
		{Name: "GH_TOKEN", Value: "gho_hib.def-123"},
		{Name: "API_KEY", Value: "sk-hib_ABC+/=:"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "" +
		"# BEGIN hibernaut secrets: hibd manages this block and replaces edits\n" +
		"API_KEY=sk-hib_ABC+/=:\n" +
		"GH_TOKEN=gho_hib.def-123\n" +
		"# END hibernaut secrets\n"
	if string(got) != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
}

func TestRenderEmptyBlock(t *testing.T) {
	got, err := envblock.Render(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "" +
		"# BEGIN hibernaut secrets: hibd manages this block and replaces edits\n" +
		"# END hibernaut secrets\n"
	if string(got) != want {
		t.Errorf("Render nil = %q, want %q", got, want)
	}
}

func TestRenderRefusesBadVars(t *testing.T) {
	for name, vars := range map[string][]envblock.Var{
		"bad name":    {{Name: "gh_token", Value: "gho_hib"}},
		"empty value": {{Name: "GH_TOKEN"}},
		"bad value":   {{Name: "GH_TOKEN", Value: "gho hib"}},
		"duplicate":   {{Name: "GH_TOKEN", Value: "gho_hib1"}, {Name: "GH_TOKEN", Value: "gho_hib2"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := envblock.Render(vars); err == nil {
				t.Errorf("Render = %q, want an error", got)
			}
		})
	}
}

func TestMerge(t *testing.T) {
	block := mustRender(t, []envblock.Var{{Name: "GH_TOKEN", Value: "gho_hib"}})
	replacement := mustRender(t, []envblock.Var{{Name: "API_KEY", Value: "sk-hib"}})
	for _, tc := range []struct {
		name  string
		file  string
		block []byte
		want  string
	}{
		{name: "empty file", block: block, want: string(block)},
		{name: "append after newline", file: "PATH=/usr/bin\n", block: block, want: "PATH=/usr/bin\n" + string(block)},
		{name: "append after no trailing newline", file: "PATH=/usr/bin", block: block, want: "PATH=/usr/bin\n" + string(block)},
		{name: "block at start", file: string(block) + "PATH=/usr/bin\n", block: replacement, want: string(replacement) + "PATH=/usr/bin\n"},
		{name: "block in middle", file: "A=1\n" + string(block) + "B=2\n", block: replacement, want: "A=1\n" + string(replacement) + "B=2\n"},
		{name: "block at end", file: "A=1\n" + string(block), block: replacement, want: "A=1\n" + string(replacement)},
		{name: "remove", file: "A=1\n" + string(block) + "B=2\n", want: "A=1\nB=2\n"},
		{name: "keep CRLF outside", file: "A=1\r\n" + string(block) + "B=2\r\n", block: replacement, want: "A=1\r\n" + string(replacement) + "B=2\r\n"},
		{name: "keep comments and other assignments", file: "# comment\nGH_TOKEN=real\n", block: block, want: "# comment\nGH_TOKEN=real\n" + string(block)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := envblock.Merge([]byte(tc.file), tc.block)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("Merge = %q, want %q", got, tc.want)
			}
			again, err := envblock.Merge(got, tc.block)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(again, got) {
				t.Errorf("Merge is not idempotent: next = %q", again)
			}
		})
	}
}

func TestMergeRefusesMalformedFiles(t *testing.T) {
	block := mustRender(t, []envblock.Var{{Name: "GH_TOKEN", Value: "gho_hib"}})
	begin := "# BEGIN hibernaut secrets: hibd manages this block and replaces edits\n"
	end := "# END hibernaut secrets\n"
	for name, file := range map[string]string{
		"begin without end": begin + "GH_TOKEN=gho_hib\n",
		"end before begin":  end + begin,
		"more than one":     string(block) + string(block),
		"nested begin":      begin + begin + end,
		"second end":        begin + "GH_TOKEN=gho_hib\n" + end + "user line\n" + end,
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := envblock.Merge([]byte(file), block); err == nil {
				t.Errorf("Merge = %q, want an error", got)
			}
		})
	}
}

func TestParse(t *testing.T) {
	block := mustRender(t, []envblock.Var{
		{Name: "GH_TOKEN", Value: "gho_hib"},
		{Name: "API_KEY", Value: "sk-hib"},
	})
	got, err := envblock.Parse([]byte("PATH=/usr/bin\n" + string(block)))
	if err != nil {
		t.Fatal(err)
	}
	want := []envblock.Var{
		{Name: "API_KEY", Value: "sk-hib"},
		{Name: "GH_TOKEN", Value: "gho_hib"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Parse = %#v, want %#v", got, want)
	}

	got, err = envblock.Parse([]byte("PATH=/usr/bin\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("Parse without block = %#v, want nil", got)
	}
}

func FuzzMerge(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		[]byte("PATH=/usr/bin"),
		[]byte("A=1\nB=2\n"),
		[]byte("A=1\r\nB=2\r\n"),
		[]byte(mustRender(f, []envblock.Var{{Name: "OLD", Value: "old_hib"}})),
		[]byte("# BEGIN hibernaut secrets: hibd manages this block and replaces edits\nOLD=old_hib\n# END hibernaut secrets\nuser line\n# END hibernaut secrets\n"),
	} {
		f.Add(seed)
	}
	vars := []envblock.Var{
		{Name: "API_KEY", Value: "sk-hib_123"},
		{Name: "GH_TOKEN", Value: "gho_hib456"},
	}
	block := mustRender(f, vars)
	f.Fuzz(func(t *testing.T, file []byte) {
		got, err := envblock.Merge(file, block)
		if err != nil {
			return
		}
		again, err := envblock.Merge(got, block)
		if err != nil {
			t.Fatalf("second Merge: %v", err)
		}
		if !bytes.Equal(got, again) {
			t.Fatalf("Merge is not idempotent: %q then %q", got, again)
		}
		if !outsideLinesInOrder(file, got) {
			t.Fatalf("outside lines are not kept in order: file %q merged %q", file, got)
		}
		parsed, err := envblock.Parse(got)
		if err != nil {
			t.Fatalf("Parse merged file: %v", err)
		}
		if !slices.Equal(parsed, vars) {
			t.Fatalf("Parse(Merge(file, Render(vars))) = %#v, want %#v", parsed, vars)
		}
	})
}

func mustRender(tb testing.TB, vars []envblock.Var) []byte {
	tb.Helper()
	block, err := envblock.Render(vars)
	if err != nil {
		tb.Fatal(err)
	}
	return block
}

func outsideLinesInOrder(file, merged []byte) bool {
	want := outsideLineTexts(file)
	got := outsideLineTexts(merged)
	j := 0
	for _, line := range got {
		if j < len(want) && line == want[j] {
			j++
		}
	}
	return j == len(want)
}

func outsideLineTexts(file []byte) []string {
	const (
		begin = "# BEGIN hibernaut secrets: hibd manages this block and replaces edits"
		end   = "# END hibernaut secrets"
	)
	var out []string
	inBlock := false
	sawBlock := false
	for _, line := range strings.SplitAfter(string(file), "\n") {
		text := strings.TrimSuffix(line, "\n")
		if !sawBlock && text == begin {
			inBlock = true
			sawBlock = true
			continue
		}
		if inBlock {
			if text == end {
				inBlock = false
			}
			continue
		}
		if line != "" {
			out = append(out, strings.TrimSuffix(line, "\n"))
		}
	}
	return out
}
