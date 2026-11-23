package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/hibapi"
)

func TestRecipeClientCRUDAndDefault(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h, true)
	ctx := taskCallContext(t)
	command := []string{"sh", "-c", "echo 'from recipe'"}
	if code := c.hibContext(ctx, append([]string{"recipe", "put", "check", "--image", "custom",
		"--size", "small", "--timeout", "1h", "--"}, command...)...); code != 0 {
		t.Fatalf("put = %d, %s; want saved recipe", code, c.errOut)
	}
	if code := c.hibContext(ctx, "recipe", "show", "check", "--json"); code != 0 {
		t.Fatal(c.errOut)
	}
	var reply hibapi.Response
	if err := json.Unmarshal(c.out.Bytes(), &reply); err != nil || reply.Recipe == nil ||
		!slices.Equal(reply.Recipe.Command, command) || reply.Recipe.Image != "custom" ||
		reply.Recipe.Size != "small" || reply.Recipe.TimeoutSeconds != 3600 {
		t.Fatalf("show = %s, %v; want quoted argv and defaults", c.out, err)
	}
	for _, args := range [][]string{{"ls"}, {"show", "check"}, {"default", "check"}, {"default"}} {
		if code := c.hibContext(ctx, append([]string{"recipe"}, args...)...); code != 0 ||
			!strings.Contains(c.out.String(), "check") {
			t.Fatalf("recipe %v = %d, stdout %q, stderr %q", args, code, c.out, c.errOut)
		}
	}
	if code := c.hibContext(ctx, "task", "start"); code != 0 {
		t.Fatalf("default start = %d, %s; want queued task", code, c.errOut)
	}
	if code := c.hibContext(ctx, "recipe", "rm", "check"); code != 0 {
		t.Fatal(c.errOut)
	}
	if code := c.hibContext(ctx, "recipe", "default"); code != 0 || !strings.Contains(c.out.String(), "no default") {
		t.Fatalf("removed default = %d, %s, %s", code, c.out, c.errOut)
	}
	if code := c.hibContext(ctx, "task", "start"); code != 1 {
		t.Fatalf("no default = %d; want 1", code)
	}
	for _, want := range []string{"no default recipe", "-- CMD", "--recipe NAME", "hib recipe default NAME"} {
		if !strings.Contains(c.errOut.String(), want) {
			t.Errorf("no default error %q lacks %q", c.errOut, want)
		}
	}
}

func TestRecipeShowDefaults(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h, true)
	ctx := taskCallContext(t)
	if code := c.hibContext(ctx, "recipe", "put", "check", "--", "true"); code != 0 {
		t.Fatal(c.errOut)
	}
	if code := c.hibContext(ctx, "recipe", "show", "check"); code != 0 {
		t.Fatalf("show = %d, %s", code, c.errOut)
	}
	for _, want := range []string{"image (default)\n", "size (default)\n", "timeout (default)\n"} {
		if !strings.Contains(c.out.String(), want) {
			t.Errorf("show = %q; want %q", c.out, want)
		}
	}
}

func TestRecipeJSONReplyUnchanged(t *testing.T) {
	raw := "{ \"v\":1, \"defaultRecipe\":\"check\", \"recipe\":{\"name\":\"check\",\"command\":[\"true\"]} }\n"
	for _, args := range [][]string{{"put", "check", "--", "true"}, {"ls"}, {"show", "check"}, {"rm", "check"}, {"default"}, {"default", "check"}} {
		h := &taskCallHost{features: `["tasks","recipes"]`, replies: []taskHostReply{{text: raw}}}
		a, out, stderr := taskStartClient(t, h)
		code := a.run(taskCallContext(t), append([]string{"recipe", "--json"}, args...))
		if code != 0 || out.String() != raw || stderr.Len() != 0 || h.versions != 1 || len(h.calls) != 1 {
			t.Fatalf("%v = %d, stdout %q, stderr %q, %d versions, calls %v; want exact JSON and two SSH calls",
				args, code, out, stderr, h.versions, h.calls)
		}
	}
}

func TestRecipeRequiresFeature(t *testing.T) {
	for _, args := range [][]string{{"recipe", "put", "check", "--", "true"},
		{"recipe", "ls"}, {"recipe", "show", "check"}, {"recipe", "rm", "check"},
		{"recipe", "default"}, {"recipe", "default", "check"},
		{"task", "start", "--recipe", "check"}, {"task", "start"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{text: startReceipt}}}
			a, out, stderr := taskStartClient(t, h)
			if code := a.run(taskCallContext(t), args); code != 1 || out.Len() != 0 ||
				!strings.Contains(stderr.String(), "Update the host") || h.versions != 1 || len(h.calls) != 0 {
				t.Fatalf("old host = exit %d, stdout %q, stderr %q, %d versions, calls %v; want only feature check",
					code, out, stderr, h.versions, h.calls)
			}
		})
	}
}

func TestRecipeClientRefusesNamesLocally(t *testing.T) {
	for _, name := range []string{"../x", "a/b", ".", "X", strings.Repeat("a", 64)} {
		for _, prefix := range [][]string{{"recipe", "put"}, {"recipe", "show"}, {"recipe", "rm"}, {"recipe", "default"}, {"task", "start", "--recipe"}} {
			args := append(slices.Clone(prefix), name)
			if prefix[1] == "put" {
				args = append(args, "--", "true")
			}
			h := &taskCallHost{features: `["tasks","recipes"]`}
			a, out, stderr := taskStartClient(t, h)
			if code := a.run(taskCallContext(t), args); code != 2 || out.Len() != 0 ||
				!strings.Contains(stderr.String(), "bad name") || h.versions != 0 || len(h.calls) != 0 {
				t.Fatalf("%v = %d, stdout %q, stderr %q, %d versions; want local usage refusal",
					args, code, out, stderr, h.versions)
			}
		}
	}
}

func TestRecipeStartWireAndCalls(t *testing.T) {
	for _, useRecipe := range []bool{false, true} {
		h := &taskCallHost{features: `["tasks","recipes"]`, replies: []taskHostReply{{text: startReceipt}}}
		a, out, stderr := taskStartClient(t, h)
		args := []string{"--request-id", "start-1", "--image", "other", "--size", "large", "--timeout", "24h"}
		if useRecipe {
			args = append(args, "--recipe", "check", "--", "echo", "override")
		}
		if code := startTask(t, a, args...); code != 0 || stderr.Len() != 0 || out.Len() == 0 {
			t.Fatalf("recipe start = %d, %s, %s", code, out, stderr)
		}
		if h.versions != 1 || len(h.calls) != 1 {
			t.Fatalf("recipe start made %d version calls, %v; want two SSH calls", h.versions, h.calls)
		}
		req, _ := startWire(t, h.inputs[0])
		if req.Image != "other" || req.Size != "large" || req.TimeoutSeconds != 86400 ||
			(useRecipe && (req.Recipe != "check" || !slices.Equal(req.Command, []string{"echo", "override"}))) {
			t.Fatalf("recipe request %+v; want explicit overrides", req)
		}
	}
}

func TestRecipeStartResendCalls(t *testing.T) {
	for _, args := range [][]string{{}, {"--recipe", "check"}} {
		h := &taskCallHost{features: `["tasks","recipes"]`, replies: []taskHostReply{
			{err: errors.New("reply lost")}, {text: startReceipt},
		}}
		a, _, stderr := taskStartClient(t, h)
		if code := startTask(t, a, append([]string{"--request-id", "start-1"}, args...)...); code != 0 || stderr.Len() != 0 ||
			h.versions != 1 || len(h.calls) != 2 {
			t.Fatalf("resend %v = %d, %s, %d versions, %d sends; want one version and two sends",
				args, code, stderr, h.versions, len(h.calls))
		}
	}
}

func TestRecipeFeatureCacheStillRequiresTasks(t *testing.T) {
	h := &taskCallHost{features: `["recipes"]`}
	a, _, stderr := taskStartClient(t, h)
	if code := startTask(t, a, "--recipe", "check"); code != 1 ||
		!strings.Contains(stderr.String(), "Update the host") || h.versions != 2 || len(h.calls) != 0 {
		t.Fatalf("recipes without tasks = %d, %s, %d versions, %d sends; want task feature refusal",
			code, stderr, h.versions, len(h.calls))
	}
}

func TestRecipeExplicitCommandCalls(t *testing.T) {
	for _, features := range []string{`["tasks"]`, `["tasks","recipes"]`} {
		h := &taskCallHost{features: features, replies: []taskHostReply{{text: startReceipt}}}
		a, _, stderr := taskStartClient(t, h)
		if code := startTask(t, a, "--request-id", "start-1", "--", "true"); code != 0 || h.versions != 1 || len(h.calls) != 1 {
			t.Fatalf("explicit start = %d, %s, %d versions, %d sends; want two SSH calls",
				code, stderr, h.versions, len(h.calls))
		}
	}
}

func TestRecipeLostChangeReplyHint(t *testing.T) {
	for _, tc := range []struct {
		args []string
		show string
	}{
		{[]string{"put", "check", "--", "true"}, "hib recipe show check"},
		{[]string{"rm", "check"}, "hib recipe show check"},
		{[]string{"default", "check"}, "hib recipe default"},
		{[]string{"show", "check"}, ""},
		{[]string{"ls"}, ""},
		{[]string{"default"}, ""},
	} {
		for _, asJSON := range []bool{false, true} {
			for _, failure := range []struct {
				reply taskHostReply
				hint  bool
			}{
				{taskHostReply{err: errors.New("reply dropped")}, true},
				{taskHostReply{err: context.DeadlineExceeded}, true},
				{taskHostReply{err: context.Canceled}, true},
				{taskHostReply{code: 255}, true},
				{taskHostReply{code: 2}, true},
				{taskHostReply{code: 1, text: `{"v":`}, true},
				{taskHostReply{code: 1, text: `{"v":1,"error":"host refused","errorCode":"BAD_REQUEST"}`}, false},
				{taskHostReply{code: 1}, false},
				{taskHostReply{code: 127}, false},
				{taskHostReply{code: -1, err: &exec.Error{Name: "ssh", Err: exec.ErrNotFound}}, false},
				{taskHostReply{code: -1, err: &os.PathError{Op: "fork/exec", Path: "ssh", Err: os.ErrPermission}}, false},
				{taskHostReply{code: -1, err: &os.PathError{Op: "read", Path: "spool", Err: os.ErrClosed}}, true},
			} {
				h := &taskCallHost{features: `["tasks","recipes"]`, replies: []taskHostReply{failure.reply}}
				a, out, stderr := taskStartClient(t, h)
				args := append([]string{"recipe"}, tc.args...)
				if asJSON {
					args = append([]string{"recipe", "--json"}, tc.args...)
				}
				code := a.run(taskCallContext(t), args)
				uncertain := tc.show != "" && failure.hint
				message := out.String() + stderr.String()
				if code != 1 || strings.Contains(message, "may have landed") != uncertain ||
					(uncertain && !strings.Contains(message, tc.show)) || len(h.calls) != 1 {
					t.Errorf("%v json=%v reply=%+v = exit %d, stdout %q, stderr %q; want ambiguity %v and %q",
						tc.args, asJSON, failure, code, out, stderr, uncertain, tc.show)
				}
				if asJSON {
					var r hibapi.Response
					if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.Error == "" || stderr.Len() != 0 {
						t.Errorf("JSON failure = %s, stderr %q, %v; want one error response", out, stderr, err)
					}
				}
			}
		}
	}
}

func TestRecipeStartLostReplyAfterEdit(t *testing.T) {
	for _, change := range []string{"edit", "rm"} {
		t.Run(change, func(t *testing.T) {
			for _, asJSON := range []bool{false, true} {
				t.Run(map[bool]string{false: "text", true: "json"}[asJSON], func(t *testing.T) {
					h := newHost(t)
					c := initClient(t, h, true)
					ctx := taskCallContext(t)
					if code := c.hibContext(ctx, "recipe", "put", "check", "--", "echo", "original"); code != 0 {
						t.Fatalf("seed recipe = %d, %s", code, c.errOut)
					}
					ssh, sends, requestID := c.ssh, 0, ""
					c.ssh = func(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) (int, error) {
						if in == nil {
							return ssh(ctx, args, in, out, stderr)
						}
						input, err := io.ReadAll(in)
						if err != nil {
							return -1, err
						}
						req, _ := startWire(t, input)
						requestID = req.RequestID
						sends++
						if sends != 1 {
							return ssh(ctx, args, bytes.NewReader(input), out, stderr)
						}
						var saved bytes.Buffer
						if code, err := ssh(ctx, args, bytes.NewReader(input), &saved, stderr); code != 0 || err != nil {
							t.Fatalf("first save = %d, %v, %s", code, err, &saved)
						}
						leaf := "put"
						if change == "rm" {
							leaf = "rm"
						}
						edit, err := (&config{Host: testHost}).sudoHibdArgs("recipe", leaf, "check", "-json")
						if err != nil {
							t.Fatal(err)
						}
						if code, err := ssh(ctx, edit, strings.NewReader(`{"command":["false"]}`), io.Discard, stderr); code != 0 || err != nil {
							t.Fatalf("recipe edit = %d, %v", code, err)
						}
						return -1, errors.New("saved but reply lost")
					}
					args := []string{"task", "start", "--recipe", "check"}
					if asJSON {
						args = append(args, "--json")
					}
					code := c.hibContext(ctx, args...)
					if code != 1 || sends != 2 || requestID == "" ||
						!strings.Contains(c.errOut.String(), "hib task show --request '"+requestID+"'") {
						t.Fatalf("recipe resend = %d, stdout %q, stderr %q, sends %d, ID %q; want saved-task lookup",
							code, c.out, c.errOut, sends, requestID)
					}
					if asJSON && !strings.Contains(c.out.String(), `"errorCode":"CONFLICT"`) {
						t.Fatalf("JSON conflict = %s; want host's coded reply", c.out)
					}
					c.ssh = ssh
					if code := c.hibContext(ctx, "task", "show", "--request", requestID); code != 0 ||
						!strings.Contains(c.out.String(), "original") || !strings.Contains(c.out.String(), "recipe check") {
						t.Fatalf("lookup = %d, %s, %s; want original task", code, c.out, c.errOut)
					}
					if change == "rm" {
						if code := c.hibContext(ctx, "task", "start", "--request-id", "new-request", "--recipe", "check"); code != 1 ||
							!strings.Contains(c.errOut.String(), `no recipe "check"`) ||
							strings.Contains(c.errOut.String(), "hib task show --request") {
							t.Fatalf("new request = %d, stdout %q, stderr %q; want recipe error without lookup", code, c.out, c.errOut)
						}
					}
				})
			}
		})
	}
}
