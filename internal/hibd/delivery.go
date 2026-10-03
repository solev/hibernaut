package hibd

import (
	"context"
	"crypto/sha256"
	"strings"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/machines"
)

type blockDelivery struct {
	engine    engine.Engine
	vaultPath string
	logf      func(format string, args ...any)
	hashes    map[string][32]byte
	problems  map[string]string
}

func newBlockDelivery(e engine.Engine, vaultPath string, logf func(format string, args ...any)) *blockDelivery {
	return &blockDelivery{engine: e, vaultPath: vaultPath, logf: logf, hashes: map[string][32]byte{}, problems: map[string]string{}}
}

func (d *blockDelivery) look(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	block, err := loadSecretBlock(d.vaultPath)
	if err != nil {
		d.report("loading secrets", err)
		return
	}
	hash := sha256.Sum256(block)
	ms, err := d.engine.List(ctx, engine.ListOptions{States: []engine.State{engine.Running}})
	if err != nil {
		d.report("listing machines", err)
		return
	}
	running := map[string]bool{}
	var stale []engine.Machine
	var wg sync.WaitGroup
	for _, m := range ms {
		if machines.Name(m) == "" {
			continue
		}
		running[m.ID] = true
		if d.hashes[m.ID] == hash {
			continue
		}
		stale = append(stale, m)
	}
	results := make([]error, len(stale))
	for i, m := range stale {
		wg.Go(func() {
			results[i] = machines.WriteBlock(ctx, d.engine, m.ID, block)
		})
	}
	wg.Wait()
	for i, m := range stale {
		name := machines.Name(m)
		err := results[i]
		if err == nil {
			d.hashes[m.ID] = hash
		}
		d.report("updating "+name, err)
	}
	for id := range d.hashes {
		if !running[id] {
			delete(d.hashes, id)
		}
	}
	for key := range d.problems {
		if strings.HasPrefix(key, "updating ") {
			continue
		}
		d.report(key, nil)
	}
}

func (d *blockDelivery) report(doing string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if d.problems[doing] == msg {
		return
	}
	d.problems[doing] = msg
	if err != nil {
		d.logf("%s: %v", doing, err)
	}
}
