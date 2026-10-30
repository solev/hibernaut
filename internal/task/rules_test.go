package task

import "testing"

func TestTaskRemoving(t *testing.T) {
	for _, kind := range []RequestKind{"", RequestCancel, RequestRescue, RequestRemove} {
		task := Task{Requests: []Request{{Kind: kind}}}
		if got := task.Removing(); got != (kind == RequestRemove) {
			t.Errorf("Removing with %q = %v", kind, got)
		}
	}
}
