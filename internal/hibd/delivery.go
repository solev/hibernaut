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
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, m := range ms {
		name := machines.Name(m)
		if name == "" {
			continue
		}
		running[m.ID] = true
		if d.hashes[m.ID] == hash {
			continue
		}
		wg.Go(func() {
			err := machines.WriteBlock(ctx, d.engine, m.ID, block)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				d.hashes[m.ID] = hash
			}
			d.report("updating "+name, err)
		})
	}
	wg.Wait()
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
