package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/hibapi"
)

func TestJSONIsOneASCIILine(t *testing.T) {
	hostile := []string{
		"plain",
		"bidi \u202e\u2066\u061c",
		"c1 \u0085\u009b",
		"emoji 😀 and 𝄞",
		"invalid \xff\xfe utf-8",
		"del \x7f esc \x1b bell \x07",
		"breaks \b\f\n\r\t and \u2028\u2029",
		`quote " backslash \ \n`,
		"html <&>",
		"accents é ü",
	}
	for _, s := range hostile {
		r := hibapi.Response{Error: s, Machines: []hibapi.Machine{{Name: s, ID: s, State: s}}, Features: []string{s}}
		var out bytes.Buffer
		if err := writeJSON(&out, r); err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		line := out.Bytes()
		if bytes.Count(line, []byte("\n")) != 1 || line[len(line)-1] != '\n' {
			t.Errorf("%q: not one line: %q", s, line)
		}
		for _, b := range line[:len(line)-1] {
			if b < 0x20 || b > 0x7e {
				t.Errorf("%q: byte %#x in %q", s, b, line)
				break
			}
		}
		var got hibapi.Response
		if err := json.Unmarshal(line, &got); err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		r.V = hibapi.Version
		want := r
		if !strings.Contains(s, "invalid") {
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%q came back as %+v", s, got)
			}
			continue
		}
		std, _ := json.Marshal(r)
		want = hibapi.Response{}
		if err := json.Unmarshal(std, &want); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q came back as %+v, want %+v as encoding/json gives", s, got, want)
		}
	}
}
