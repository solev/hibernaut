package hibd

import (
	"strings"
	"testing"
)

func TestForkHintsThatCopiesDontInheritPorts(t *testing.T) {
	const hint = "copies don't inherit published ports. hib port add web-1 8080 publishes one\n"
	for _, tc := range []struct {
		name      string
		on        bool
		publishes bool
		json      bool
		want      bool
	}{
		{name: "parent publishes", on: true, publishes: true, want: true},
		{name: "parent publishes, json", on: true, publishes: true, json: true, want: true},
		{name: "parent publishes nothing", on: true},
		{name: "links off", publishes: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, f, out, errOut, id := hostsApp(t, tc.on)
			if !tc.publishes {
				f.doc.Machines[id].Ports = nil
			}
			args := []string{"fork", "web"}
			if tc.json {
				madeBy(t, a, out, errOut, append(args, "-json")...)
			} else if code := a.Run(t.Context(), args); code != 0 {
				t.Fatalf("fork = %d; stderr %s", code, errOut)
			}
			if got := strings.Count(errOut.String(), hint); got != map[bool]int{true: 1}[tc.want] {
				t.Errorf("stderr = %q, want the hint %v", errOut, tc.want)
			}
			if strings.Contains(out.String(), "inherit") {
				t.Errorf("stdout = %q, want the hint on stderr only", out)
			}
		})
	}
}
