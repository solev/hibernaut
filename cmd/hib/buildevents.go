package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
)

const maxEventLine = 64 << 10

var errEventLineTooLong = errors.New("hibd sent a line longer than 64 KiB")

func (a *app) features(ctx context.Context, cfg *config) ([]string, error) {
	r, err := a.remote(ctx, cfg, shortTimeout, "version", "--json")
	if err != nil {
		return nil, err
	}
	return r.Features, nil
}

func (a *app) buildImage(ctx context.Context, cfg *config, name string, remote []string, verbose bool) (hibapi.Response, error) {
	if !verbose && !a.liveTerminal() {
		return a.remote(ctx, cfg, buildTimeout, remote...)
	}
	features, err := a.features(ctx, cfg)
	switch {
	case err != nil:
		return hibapi.Response{}, err
	case !slices.Contains(features, hibapi.FeatureBuildEvents):
		return a.remote(ctx, cfg, buildTimeout, remote...)
	case verbose:
		return a.remote(ctx, cfg, buildTimeout, append(remote, "--verbose")...)
	}
	building := "building " + name
	defer a.showLive(ctx, building, buildingFace)()
	return a.call(ctx, cfg, buildTimeout, cfg.sshArgs(append(remote, "--events")...), callOptions{
		mayWake: true,
		events: func(e hibapi.Event) {
			if e.State == hibapi.EventStarted {
				a.live.set(stepPrefix(e) + termsafe.Line(e.Text))
				return
			}
			fmt.Fprintln(a.stderr, stepLine(e))
			a.live.set(building)
		},
	})
}

func stepPrefix(e hibapi.Event) string {
	return fmt.Sprintf("%s (%s)  %s  ", termsafe.Line(e.Image), termsafe.Line(e.Size), termsafe.Line(e.Step))
}

func stepLine(e hibapi.Event) string {
	prefix := stepPrefix(e)
	result := termsafe.Line(string(e.State))
	if e.State == hibapi.EventBuilt {
		result = fmt.Sprintf("built in %ds", e.Seconds)
	}
	suffix := "  " + result
	return prefix + shortenLine(termsafe.Line(e.Text), 100-len(prefix)-len(suffix)) + suffix
}

type buildEvents struct {
	show    func(hibapi.Event)
	reply   io.Writer
	cancel  context.CancelFunc
	partial []byte
	lines   int
	replied bool
	err     error
}

func (b *buildEvents) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	b.partial = append(b.partial, p...)
	for b.err == nil {
		end := bytes.IndexByte(b.partial, '\n')
		if end < 0 {
			break
		}
		b.line(b.partial[:end+1])
		b.partial = b.partial[end+1:]
	}
	if b.err == nil && len(b.partial) >= maxEventLine {
		b.err = errEventLineTooLong
	}
	if b.err != nil {
		b.cancel()
		return 0, b.err
	}
	return len(p), nil
}

func (b *buildEvents) line(data []byte) {
	b.lines++
	if len(data) > maxEventLine {
		b.err = errEventLineTooLong
		return
	}
	if b.replied {
		b.err = errors.New("hibd sent more after its reply")
		return
	}
	r, err := hibapi.DecodeResponse(data)
	var remote *hibapi.RemoteError
	switch {
	case err == nil && r.Event != nil:
		b.show(*r.Event)
	case err == nil, errors.As(err, &remote), errors.Is(err, hibapi.ErrVersion):
		b.replied = true
		b.reply.Write(data)
	default:
		b.err = err
	}
}

func (b *buildEvents) heard() bool {
	return b != nil && b.lines > 0
}

func (b *buildEvents) end(sshErr error) error {
	if b.err == nil && sshErr == nil && len(b.partial) > 0 {
		b.line(b.partial)
	}
	const mayBeRunning = "The build may still be running on the host: check hib image ls in a few minutes"
	switch {
	case b.err != nil:
		return fmt.Errorf("couldn't read hibd's build progress: %w. %s", b.err, mayBeRunning)
	case sshErr != nil:
		return sshErr
	case b.lines > 0 && !b.replied:
		return errors.New("hibd stopped before it said how the build ended. " + mayBeRunning)
	}
	return nil
}
