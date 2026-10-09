package hostsblock_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/hostsblock"
)

const (
	begin = "# BEGIN hibernaut machines: hibd manages this block and replaces edits\n"
	end   = "# END hibernaut machines\n"
	base  = "127.0.0.1 localhost\n::1 localhost ip6-localhost ip6-loopback\n127.0.1.1 e2b.local\n"
)

func TestRenderSortsByName(t *testing.T) {
	got, err := hostsblock.Render([]hostsblock.Host{
		{IP: "198.18.0.2", Name: "web.hib"},
		{IP: "198.18.0.1", Name: "api.hib"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := begin + "198.18.0.1 api.hib\n198.18.0.2 web.hib\n" + end; string(got) != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
}

func TestRenderReturnsNilWithoutHosts(t *testing.T) {
	got, err := hostsblock.Render(nil)
	if err != nil || got != nil {
		t.Errorf("Render(nil) = %q, %v, want nil", got, err)
	}
}

func TestRenderRefusesBadHosts(t *testing.T) {
	for name, tc := range map[string]struct {
		hosts []hostsblock.Host
		want  error
	}{
		"localhost":         {[]hostsblock.Host{{IP: "198.18.0.1", Name: "localhost"}}, hostsblock.ErrReservedName},
		"LOCALHOST":         {[]hostsblock.Host{{IP: "198.18.0.1", Name: "LocalHost"}}, hostsblock.ErrReservedName},
		"ip6-localhost":     {[]hostsblock.Host{{IP: "198.18.0.1", Name: "ip6-localhost"}}, hostsblock.ErrReservedName},
		"e2b hostname":      {[]hostsblock.Host{{IP: "198.18.0.1", Name: "e2b.local"}}, hostsblock.ErrReservedName},
		"loopback as name":  {[]hostsblock.Host{{IP: "198.18.0.1", Name: "127.0.0.1"}}, hostsblock.ErrReservedName},
		"v6 loopback":       {[]hostsblock.Host{{IP: "198.18.0.1", Name: "::1"}}, hostsblock.ErrReservedName},
		"no suffix":         {[]hostsblock.Host{{IP: "198.18.0.1", Name: "api"}}, hostsblock.ErrBadName},
		"other suffix":      {[]hostsblock.Host{{IP: "198.18.0.1", Name: "api.local"}}, hostsblock.ErrBadName},
		"upper case":        {[]hostsblock.Host{{IP: "198.18.0.1", Name: "API.hib"}}, hostsblock.ErrBadName},
		"two labels":        {[]hostsblock.Host{{IP: "198.18.0.1", Name: "a.api.hib"}}, hostsblock.ErrBadName},
		"leading hyphen":    {[]hostsblock.Host{{IP: "198.18.0.1", Name: "-api.hib"}}, hostsblock.ErrBadName},
		"space":             {[]hostsblock.Host{{IP: "198.18.0.1", Name: "api.hib evil"}}, hostsblock.ErrBadName},
		"newline":           {[]hostsblock.Host{{IP: "198.18.0.1", Name: "api.hib\n127.0.0.1"}}, hostsblock.ErrBadName},
		"label too long":    {[]hostsblock.Host{{IP: "198.18.0.1", Name: strings.Repeat("a", 64) + ".hib"}}, hostsblock.ErrBadName},
		"loopback address":  {[]hostsblock.Host{{IP: "127.0.0.1", Name: "api.hib"}}, hostsblock.ErrBadIP},
		"private address":   {[]hostsblock.Host{{IP: "10.0.0.1", Name: "api.hib"}}, hostsblock.ErrBadIP},
		"below first":       {[]hostsblock.Host{{IP: "198.18.0.0", Name: "api.hib"}}, hostsblock.ErrBadIP},
		"past the range":    {[]hostsblock.Host{{IP: "198.20.0.1", Name: "api.hib"}}, hostsblock.ErrBadIP},
		"v6":                {[]hostsblock.Host{{IP: "::1", Name: "api.hib"}}, hostsblock.ErrBadIP},
		"v4 in v6":          {[]hostsblock.Host{{IP: "::ffff:198.18.0.1", Name: "api.hib"}}, hostsblock.ErrBadIP},
		"leading zero":      {[]hostsblock.Host{{IP: "198.18.0.01", Name: "api.hib"}}, hostsblock.ErrBadIP},
		"not an address":    {[]hostsblock.Host{{IP: "api", Name: "api.hib"}}, hostsblock.ErrBadIP},
		"duplicate name":    {[]hostsblock.Host{{IP: "198.18.0.1", Name: "api.hib"}, {IP: "198.18.0.2", Name: "api.hib"}}, hostsblock.ErrDuplicate},
		"duplicate address": {[]hostsblock.Host{{IP: "198.18.0.1", Name: "api.hib"}, {IP: "198.18.0.1", Name: "web.hib"}}, hostsblock.ErrDuplicate},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := hostsblock.Render(tc.hosts); !errors.Is(err, tc.want) {
				t.Errorf("Render = %q, %v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestRenderAcceptsTheWholeRange(t *testing.T) {
	for _, ip := range []string{"198.18.0.1", "198.18.255.255", "198.19.0.0", "198.19.255.254"} {
		if _, err := hostsblock.Render([]hostsblock.Host{{IP: ip, Name: "api.hib"}}); err != nil {
			t.Errorf("Render with %s: %v", ip, err)
		}
	}
	if _, err := hostsblock.Render([]hostsblock.Host{{IP: "198.18.0.1", Name: strings.Repeat("a", 63) + ".hib"}}); err != nil {
		t.Errorf("Render with a 63-letter name: %v", err)
	}
}

func TestMerge(t *testing.T) {
	one := mustRender(t, hostsblock.Host{IP: "198.18.0.1", Name: "api.hib"})
	two := mustRender(t, hostsblock.Host{IP: "198.18.0.1", Name: "api.hib"}, hostsblock.Host{IP: "198.18.0.2", Name: "web.hib"})
	for name, tc := range map[string]struct {
		file  string
		block []byte
		want  string
	}{
		"append":                     {file: base, block: one, want: base + string(one)},
		"append to an empty file":    {file: "", block: one, want: string(one)},
		"append without a newline":   {file: "127.0.0.1 localhost", block: one, want: "127.0.0.1 localhost\n" + string(one)},
		"replace keeps lines around": {file: base + string(one) + "10.0.0.5 db\n", block: two, want: base + string(two) + "10.0.0.5 db\n"},
		"replace edits in the block": {file: base + begin + "127.0.0.1 api.hib\n" + end, block: one, want: base + string(one)},
		"remove with nil":            {file: base + string(one) + "10.0.0.5 db\n", block: nil, want: base + "10.0.0.5 db\n"},
		"remove with empty":          {file: base + string(two), block: []byte{}, want: base},
		"remove without a block":     {file: base, block: nil, want: base},
		"keep CRLF outside":          {file: "127.0.0.1 localhost\r\n" + string(one) + "10.0.0.5 db\r\n", block: two, want: "127.0.0.1 localhost\r\n" + string(two) + "10.0.0.5 db\r\n"},
		"CRLF markers":               {file: base + strings.ReplaceAll(string(one), "\n", "\r\n"), block: two, want: base + string(two)},
		"end marker at EOF":          {file: base + strings.TrimSuffix(string(one), "\n"), block: two, want: base + string(two)},
		"second block goes":          {file: base + string(one) + "10.0.0.5 db\n" + string(one), block: two, want: base + string(two) + "10.0.0.5 db\n"},
		"remove every block":         {file: string(one) + base + string(two), block: nil, want: base},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := hostsblock.Merge([]byte(tc.file), tc.block)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("Merge = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMergeRefusesUnterminatedBlocks(t *testing.T) {
	one := mustRender(t, hostsblock.Host{IP: "198.18.0.1", Name: "api.hib"})
	for name, file := range map[string]string{
		"begin without end": base + begin + "198.18.0.1 api.hib\n",
		"end without begin": base + "198.18.0.1 api.hib\n" + end,
		"nested begin":      base + begin + begin + end,
		"end before begin":  base + end + begin,
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := hostsblock.Merge([]byte(file), one); !errors.Is(err, hostsblock.ErrUnterminated) {
				t.Errorf("Merge = %q, %v, want %v", got, err, hostsblock.ErrUnterminated)
			}
			if _, err := hostsblock.Merge([]byte(file), nil); !errors.Is(err, hostsblock.ErrUnterminated) {
				t.Errorf("Merge removing = %v, want %v", err, hostsblock.ErrUnterminated)
			}
		})
	}
}

func TestMergeRefusesBlocksRenderDidNotReturn(t *testing.T) {
	for name, tc := range map[string]struct {
		block string
		want  error
	}{
		"localhost":         {begin + "198.18.0.1 localhost\n" + end, hostsblock.ErrReservedName},
		"loopback address":  {begin + "127.0.0.1 api.hib\n" + end, hostsblock.ErrBadIP},
		"bad name":          {begin + "198.18.0.1 api\n" + end, hostsblock.ErrBadName},
		"duplicate name":    {begin + "198.18.0.1 api.hib\n198.18.0.2 api.hib\n" + end, hostsblock.ErrDuplicate},
		"alias":             {begin + "198.18.0.1 api.hib localhost\n" + end, nil},
		"unsorted":          {begin + "198.18.0.2 web.hib\n198.18.0.1 api.hib\n" + end, nil},
		"two spaces":        {begin + "198.18.0.1  api.hib\n" + end, nil},
		"comment":           {begin + "# 198.18.0.1 api.hib\n" + end, nil},
		"no hosts":          {begin + end, nil},
		"no end marker":     {begin + "198.18.0.1 api.hib\n", nil},
		"no newline at end": {begin + "198.18.0.1 api.hib\n" + strings.TrimSuffix(end, "\n"), nil},
		"text after":        {begin + "198.18.0.1 api.hib\n" + end + "127.0.0.1 api.hib\n", nil},
		"text before":       {"127.0.0.1 api.hib\n" + begin + "198.18.0.1 api.hib\n" + end, nil},
		"no markers":        {"198.18.0.1 api.hib\n", nil},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := hostsblock.Merge([]byte(base), []byte(tc.block))
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Errorf("Merge = %q, %v, want an error like %v", got, err, tc.want)
			}
		})
	}
}

func mustRender(t *testing.T, hosts ...hostsblock.Host) []byte {
	t.Helper()
	b, err := hostsblock.Render(hosts)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
