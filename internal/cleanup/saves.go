package cleanup

import "time"

// SaveRecord describes a dispatched image save that cleanup has not yet accounted for.
type SaveRecord struct {
	Machine   string    `json:"machine"`
	Name      string    `json:"name"`
	Started   time.Time `json:"started"`
	BootID    string    `json:"boot_id"`
	Ended     time.Time `json:"ended"`
	Succeeded bool      `json:"succeeded"`
}

// SaveSettled reports whether the listing proves that a save can no longer hide a running input.
func SaveSettled(l Listing, r SaveRecord, bootID string, callTimeout time.Duration) bool {
	if r.BootID != "" && bootID != "" && r.BootID != bootID {
		return true
	}
	if r.Succeeded {
		for _, m := range l.Machines {
			if m.ID == r.Machine && !m.Paused {
				return true
			}
		}
	}
	hasSlot, allDeleted := false, true
	for _, e := range l.Envs {
		if e.Machine == r.Machine {
			hasSlot = true
			allDeleted = allDeleted && !e.Deleted.IsZero()
		}
	}
	if hasSlot && allDeleted {
		return true
	}
	after := r.Ended
	if after.IsZero() {
		after = r.Started.Add(callTimeout)
	}
	envs := indexEnvs(l.Envs)
	for _, b := range l.Builds {
		if b.Status != "ready" || !b.Created.After(after) {
			continue
		}
		inMachine, inImage := false, false
		for _, a := range b.In {
			e := envs[a.Env]
			inMachine = inMachine || e.Machine == r.Machine
			inImage = inImage || e.Machine == ""
		}
		if inMachine && !inImage {
			return true
		}
	}
	return false
}
