package tasks

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

// ErrLogRange means the requested run or cursor does not exist.
var ErrLogRange = errors.New("invalid task log range")

// Logs reads at most 1 MiB from one stage of the run-wide stream. n is 0 for the latest run.
func (t *Tasks) Logs(ctx context.Context, id task.ID, n int, from int64) (hibapi.TaskLog, error) {
	record, err := t.Task(ctx, id)
	if err != nil {
		return hibapi.TaskLog{}, err
	}
	if n == 0 && len(record.Runs) > 0 {
		n = record.Runs[len(record.Runs)-1].ID.N
	}
	for _, r := range record.Runs {
		if r.ID.N == n {
			return readLogs(taskstore.FileLogs{Dir: t.dir}, r, from)
		}
	}
	return hibapi.TaskLog{}, fmt.Errorf("%w: no run %d", ErrLogRange, n)
}

func readLogs(logs taskstore.Logs, r task.Run, from int64) (hibapi.TaskLog, error) {
	ended := r.Ended()
	stages := task.Stages()
	sizes := make([]int64, len(stages))
	var end int64
	// Read successors first so a newly copied successor cannot hide its predecessor's tail.
	for i := len(stages) - 1; i >= 0; i-- {
		size, err := logs.Size(r.ID, stages[i])
		if err != nil {
			return hibapi.TaskLog{}, err
		}
		sizes[i] = size
		end += size
	}
	if from < 0 || from > end {
		return hibapi.TaskLog{}, fmt.Errorf("%w: byte %d is outside 0 to %d", ErrLogRange, from, end)
	}
	out := hibapi.TaskLog{Run: r.ID.N, From: from, Next: from, Data: []byte{}}
	var start int64
	for i, stage := range stages {
		if from < start+sizes[i] {
			out.Stage = stage
			reader, err := logs.Open(r.ID, stage, from-start)
			if err != nil {
				return hibapi.TaskLog{}, err
			}
			data := make([]byte, min(1<<20, start+sizes[i]-from))
			_, err = io.ReadFull(reader, data)
			closeErr := reader.Close()
			if err != nil {
				return hibapi.TaskLog{}, err
			}
			if closeErr != nil {
				return hibapi.TaskLog{}, closeErr
			}
			out.Data = data
			out.Next += int64(len(data))
			break
		}
		start += sizes[i]
	}
	out.Done = ended && out.Next == end
	return out, nil
}
