package task

import (
	"strings"
	"testing"
)

func TestUnreadPublicationKeepDetail(t *testing.T) {
	for _, tc := range []struct {
		name string
		keep *KeepResult
		want string
	}{
		{"kept", &KeepResult{}, "the work is in the kept machine"},
		{"gone", &KeepResult{Gone: true}, "the work was not kept"},
		{"removed", &KeepResult{Removed: true}, "the work was not kept"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Run{Spec: Spec{Repo: Repo{Owner: "o", Name: "r"}, Publish: PublishDraftPR}}
			for _, b := range []Body{&MachineResult{ID: "m"},
				&StageExit{Stage: StageFinish, Exit: ExitRecord{Result: "success"}},
				&CollectResult{}, &PublishResult{}} {
				r.Log = append(r.Log, Checkpoint{Body: b})
			}
			r.Log = append(r.Log, Checkpoint{Body: tc.keep})
			o, stage := r.Outcome()
			detail := r.Status("").Detail
			if o != Failed || stage != StageFinish || !strings.HasSuffix(detail, tc.want) {
				t.Fatalf("outcome=%s at %s detail=%q, want %q", o, stage, detail, tc.want)
			}
		})
	}
}
