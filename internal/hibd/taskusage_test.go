package hibd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
)

func TestMainUsageDescribesTaskAndRecipeGroups(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var out, stderr bytes.Buffer
	a := &App{Stdout: &out, Stderr: &stderr}
	if code := a.Run(ctx, nil); code != 2 || out.Len() != 0 {
		t.Fatalf("hibd without arguments = exit %d, stdout %q; want usage on stderr", code, out.String())
	}
	text := stderr.String()
	for _, description := range []string{
		"save and look after tasks the daemon runs in their own machines",
		"keep named commands for hib task start",
		"status, task, recipe and version\nprint JSON with -json.",
	} {
		if !strings.Contains(text, description) {
			t.Errorf("hibd usage lacks %q", description)
		}
	}
	for _, group := range []string{"task", "recipe"} {
		var leaves []string
		for _, row := range hibapi.TaskCommands() {
			if leaf, ok := strings.CutPrefix(row.Path, group+" "); ok {
				leaves = append(leaves, leaf)
			}
		}
		if !strings.Contains(text, "\n  "+group+" "+strings.Join(leaves, "|")+"\n") {
			t.Errorf("hibd usage lacks %s leaves in row order %q", group, leaves)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if len(line) > 100 {
			t.Errorf("hibd usage line has %d columns, want at most 100: %s", len(line), line)
		}
	}
}
