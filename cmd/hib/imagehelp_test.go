package main

import (
	"strings"
	"testing"
)

func TestSaveAndForkHelpExplainsCostAndContentsBeforeInit(t *testing.T) {
	h := newHost(t)
	c := newClient(t, h)
	for _, args := range [][]string{{"image", "save", "-h"}, {"fork", "-h"}} {
		if code := c.hib(args...); code != 0 {
			t.Fatalf("%v help exit %d; stderr %s", args, code, c.errOut)
		}
		for _, part := range []string{
			"usage: hib", "pauses for a few seconds", "SSH sessions", "may drop",
			"everything in the machine", "placeholders", "public key",
			"a secret you typed in yourself goes into the image too",
		} {
			if !strings.Contains(c.errOut.String(), part) {
				t.Errorf("%v help lacks %q: %s", args, part, c.errOut)
			}
		}
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Fatalf("help reached the host: %q", calls)
	}
}
