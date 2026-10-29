package taskstore

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
)

type stagedInput struct {
	name string
	hash task.Hash
}

type inputBudget int64

func (b *inputBudget) take(sp task.Spec) error {
	for _, f := range sp.Inputs {
		if f.Size > int64(*b) {
			return task.ErrInputFull
		}
		*b -= inputBudget(f.Size)
	}
	return nil
}

func stageInputs(d statefile.Dir, root *os.Root, owner statefile.Owner, sp *task.Spec,
	inputs io.Reader) (staged []stagedInput, err error) {
	defer func() {
		if err != nil {
			for _, f := range staged {
				root.Remove(f.name)
			}
		}
	}()
	for i, input := range sp.Inputs {
		if err := task.CheckInputName(input.Name); err != nil {
			return staged, err
		}
		if input.Size < 0 {
			return staged, fmt.Errorf("input %s: negative size", input.Name)
		}
		name := Files + "/staging/" + rand.Text()
		f, err := createPrivate(d, root, owner, name)
		if err != nil {
			return staged, err
		}
		staged = append(staged, stagedInput{name: name})
		h := sha256.New()
		_, copyErr := io.CopyN(io.MultiWriter(f, h), inputs, input.Size)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
			return staged, fmt.Errorf("staging %s: %w", input.Name, err)
		}
		hash := task.Hash(h.Sum(nil))
		sp.Inputs[i].Hash = hash
		staged[len(staged)-1].hash = hash
	}
	return staged, nil
}

func publishInputs(root *os.Root, owner statefile.Owner, staged []stagedInput) error {
	for _, f := range staged {
		name := Files + "/blobs/" + f.hash.String()
		fi, err := root.Lstat(name)
		if err == nil {
			if err := privateFile(fi, owner); err != nil {
				return err
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := root.Rename(f.name, name); err != nil {
			return err
		}
	}
	return syncDirectory(root, Files+"/blobs")
}
