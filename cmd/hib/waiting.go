package main

import (
	"context"
	"errors"
)

var errStillStarting = errors.New("stopped waiting. The host keeps starting: check hib status in a few minutes")

func (a *app) failWaiting(ctx context.Context, err error, leftBehind string) int {
	var s stopped
	switch {
	case !errors.As(context.Cause(ctx), &s), errors.Is(err, errStillStarting):
		return a.fail(err)
	case leftBehind == "":
		return a.fail(errors.New("stopped waiting"))
	}
	return a.fail(errors.New("stopped waiting. " + leftBehind))
}
