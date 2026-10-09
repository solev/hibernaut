package hook

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrStrayRule is a PREROUTING rule commented hibernaut, of a shape no version of the hook
// writes, that iptables would not delete. Ensure and Remove do the rest of their work around it
// and return an error wrapping ErrStrayRule, which is a warning: one stray rule must never stop
// the proxy or leave the hook unrepaired.
var ErrStrayRule = errors.New("hook: an unrecognised hibernaut rule could not be deleted")

// natIO reads the nat table, as iptables -t nat -S, and changes it in one iptables-restore
// --noflush transaction.
type natIO struct {
	list    func(context.Context) (natTable, error)
	restore func(context.Context, []string) error
}

// ensure moves the table to want, as Rule.Ensure describes. A transaction that fails is tried
// again without the deletes of unrecognised hibernaut rules. When the layout is then in place
// and such a rule is left, the error wraps ErrStrayRule and carries the failed transaction's
// message.
func ensure(ctx context.Context, nat natIO, want layout) (bool, error) {
	table, err := nat.list(ctx)
	if err != nil {
		return false, err
	}
	lines := plan(table, want, false)
	if lines == nil {
		return false, nil
	}
	failed := nat.restore(ctx, lines)
	if failed != nil {
		retry := plan(table, want, true)
		switch {
		case retry == nil:
			// Only unrecognised rules were to go, so the layout is in place around them.
			return false, strayError(strays(table), failed.Error())
		case slices.Equal(retry, lines):
			// The transaction deleted no unrecognised rule, so it failed for another reason.
			return false, failed
		}
		if err := nat.restore(ctx, retry); err != nil {
			return false, err
		}
	}
	table, err = nat.list(ctx)
	if err != nil {
		return true, err
	}
	if rest := plan(table, want, true); rest != nil {
		return true, fmt.Errorf("hook: iptables did not take the hibernaut layout, still to do: %q", rest)
	}
	if left := strays(table); len(left) > 0 {
		cause := "iptables-restore succeeded and the rule is still listed"
		if failed != nil {
			cause = failed.Error()
		}
		return true, strayError(left, cause)
	}
	return true, nil
}

// keep runs apply now, then every every until ctx ends, as Rule.Keep describes.
func keep(ctx context.Context, every time.Duration, logf func(string, ...any), apply func(context.Context) (bool, error)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	// A stray rule is logged once, and again only when what is said about it changes.
	var stray string
	warn := func(err error) {
		if msg := err.Error(); msg != stray {
			stray = msg
			logf("%v", err)
		}
	}
	if _, err := apply(ctx); errors.Is(err, ErrStrayRule) {
		warn(err)
	} else if err != nil {
		return err
	}
	if every <= 0 {
		every = time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			changed, err := apply(ctx)
			switch {
			case err != nil && ctx.Err() != nil:
				return nil
			case errors.Is(err, ErrStrayRule):
				warn(err)
			case err != nil:
				logf("Ensure failed: %v", err)
				continue
			default:
				stray = ""
			}
			if changed {
				logf("repaired the hibernaut iptables hook")
			}
		}
	}
}

// remove deletes everything of ours, as Remove describes. A transaction that fails is tried
// again without the deletes of unrecognised hibernaut rules, so ours always go, and the error
// then wraps ErrStrayRule and names what was left.
func remove(ctx context.Context, nat natIO) error {
	table, err := nat.list(ctx)
	if err != nil {
		return err
	}
	lines := planRemove(table, false)
	if lines == nil {
		return nil
	}
	failed := nat.restore(ctx, lines)
	if failed == nil {
		return nil
	}
	retry := planRemove(table, true)
	if slices.Equal(retry, lines) {
		return failed
	}
	if retry != nil {
		if err := nat.restore(ctx, retry); err != nil {
			return err
		}
	}
	return strayError(strays(table), failed.Error())
}

// strays returns the hibernaut rules in t of a shape no version of the hook writes.
func strays(t natTable) []natRule {
	var out []natRule
	for _, r := range t.rules {
		if r.owned() && !r.known() {
			out = append(out, r)
		}
	}
	return out
}

func strayError(left []natRule, cause string) error {
	names := make([]string, len(left))
	for i, r := range left {
		names[i] = strconv.Quote("-A " + r.chain + " " + r.spec)
	}
	return fmt.Errorf("%w: left %s in place: %s", ErrStrayRule, strings.Join(names, ", "), cause)
}
