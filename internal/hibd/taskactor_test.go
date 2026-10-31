package hibd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
)

func TestTaskStartAndCancelRecordTheSameActor(t *testing.T) {
	for _, user := range []string{"martin", ""} {
		t.Run(user, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			a, out, _ := newApp(t, fake.New())
			env := a.Getenv
			a.Getenv = func(k string) string {
				if k == "SUDO_USER" {
					return user
				}
				return env(k)
			}
			a.Stdin = taskRequestInput(t, hibapi.TaskStart{
				RequestID: "actor-start", Command: []string{"true"},
			}, strings.NewReader(""))
			code, reply, err := runJSON(t, a, out, "task", "start", "-json")
			if code != 0 || err != nil || reply.Task == nil {
				t.Fatalf("start = exit %d, %+v, %v", code, reply, err)
			}
			st := savedTasks(t, a)
			record, err := st.Task(ctx, reply.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := user
			if want == "" {
				want = "root"
			}
			actor := task.Actor{Owner: want, Name: want, Source: "cli"}
			if got := record.Runs[0].Actor; got != actor {
				t.Errorf("start actor = %+v, want %+v", got, actor)
			}
			out.Reset()
			code, reply, err = runJSON(t, a, out, "task", "cancel", "-json",
				"-request-id", "actor-cancel", string(record.ID))
			if code != 0 || err != nil || reply.Request == nil {
				t.Fatalf("cancel = exit %d, %+v, %v", code, reply, err)
			}
			record, err = st.Task(ctx, record.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := record.Requests[0].Actor; got != actor {
				t.Errorf("cancel actor = %+v, want %+v", got, actor)
			}
		})
	}
}
