package hibd

import (
	"context"
	"fmt"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/termsafe"
)

func (a *App) fork(ctx context.Context, args []string) int {
	fl := a.flags("fork")
	count := fl.Int("count", 1, fmt.Sprintf("copies to make, from 1 to %d", machines.MaxCopies))
	asJSON := fl.Bool("json", false, "print JSON")
	names, ok := a.parse(fl, args, 1, "fork MACHINE [-count N] [-json]")
	if !ok {
		return 2
	}
	if *count < 1 || *count > machines.MaxCopies {
		fmt.Fprintf(a.Stderr, "hibd fork: bad copy count %d: use 1 to %d\n", *count, machines.MaxCopies)
		return 2
	}
	if err := a.needRoot("fork"); err != nil {
		return a.fail(*asJSON, "fork", err)
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(*asJSON, "fork", err)
	}
	setup, err := loadSetup(cfg, a.codexStore(cfg, a.warnf))
	if err != nil {
		return a.fail(*asJSON, "fork", err)
	}
	a.addHosts(ctx, cfg, &setup, "the copies of "+names[0])
	m, err := a.managerWithSetup(setup)
	if err != nil {
		return a.fail(*asJSON, "fork", err)
	}
	made, err := m.Fork(ctx, names[0], *count)
	code := 0
	if *asJSON {
		r := hibapi.Response{Machines: made}
		if err != nil {
			r.Error = termsafe.String(err.Error())
		}
		code = a.reply(r)
	} else {
		hibapi.PrintFork(a.Stdout, names[0], made)
		if err != nil {
			code = a.fail(false, "fork", err)
		}
	}
	a.forkHint(ctx, cfg, names[0], made)
	return code
}

// forkHint tells whoever forked a machine that publishes ports that its copies publish none,
// and how to publish one. It goes to stderr, so -json leaves it out and hib passes it on. It
// says nothing with links off, when no copy was made, or when it can't tell, since the copies
// are there either way.
func (a *App) forkHint(ctx context.Context, cfg config, parent string, made []hibapi.Machine) {
	if !cfg.linksConfiguredOn() || len(made) == 0 {
		return
	}
	e, err := a.engine()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	em, err := machines.Lookup(ctx, e, parent)
	if err != nil {
		return
	}
	doc, err := a.linkPartsFor(cfg).store.Read()
	if err != nil {
		return
	}
	if lm := doc.Machines[em.ID]; lm != nil && len(lm.Ports) > 0 {
		a.notef("copies don't inherit published ports. hib port add %s %d publishes one", made[0].Name, lm.Ports[0])
	}
}
