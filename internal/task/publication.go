package task

import "errors"

// Publishable reports whether successful work may be published.
func (r Run) Publishable() bool {
	if r.Spec.Publish != PublishDraftPR {
		return false
	}
	if _, stopped := r.Stop(); stopped {
		return false
	}
	for _, c := range r.Log {
		if b, ok := c.Body.(*StageExit); ok && !b.Exit.OK() {
			return false
		}
	}
	return r.finishOK()
}

func (r Run) finishOK() bool {
	for _, c := range r.Log {
		if b, ok := c.Body.(*StageExit); ok && b.Stage == StageFinish {
			return b.Exit.OK()
		}
	}
	return false
}

// AtRef verifies this run's publication against the task branch on GitHub.
func (p Publication) AtRef(commit string) (Publication, error) {
	if p.Start == "" {
		return Publication{}, errors.New("publication has no command starting commit")
	}
	if commit == p.Start || (commit == "" && !p.Changes && !p.Pushed) {
		return Publication{Start: p.Start, Author: p.Author}, nil
	}
	if !p.Changes || !p.Pushed || p.Commit == "" || commit != p.Commit {
		return Publication{}, errors.New("GitHub task branch does not match this run's push")
	}
	p.PR = ""
	return p, nil
}
