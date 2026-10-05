package main

import "testing"

func TestColourOnlyOnTerminalsThatWantIt(t *testing.T) {
	for _, tc := range []struct {
		terminal bool
		env      map[string]string
		want     bool
	}{
		{true, map[string]string{"TERM": "xterm-256color"}, true},
		{true, nil, true},
		{false, map[string]string{"TERM": "xterm-256color"}, false},
		{true, map[string]string{"TERM": "dumb"}, false},
		{true, map[string]string{"NO_COLOR": "1"}, false},
		{true, map[string]string{"NO_COLOR": "yes"}, false},
		{true, map[string]string{"NO_COLOR": "false"}, false},
		{true, map[string]string{"NO_COLOR": ""}, true},
	} {
		if got := colourOn(tc.terminal, func(k string) string { return tc.env[k] }); got != tc.want {
			t.Errorf("terminal %v, env %v: colour %v, want %v", tc.terminal, tc.env, got, tc.want)
		}
	}
}
