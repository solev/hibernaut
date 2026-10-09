//go:build !linux

package linkadmit

import "time"

// runBackstop is how often the watch loop re-reads every watched cgroup. There are no E2B
// cgroups off Linux, so this only serves the tests.
const runBackstop = 100 * time.Millisecond

func newNotifier(string) notifier { return newPollNotifier(runBackstop) }
