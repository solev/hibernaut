package machines_test

import (
	"testing"

	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
)

func TestSavedCopyResetsKeysWithStableMachineID(t *testing.T) {
	g := newCopyGuest()
	v := newEnv(t, fake.WithExec(g.exec))
	v.create(t, "web")
	m := imageManager(v, nil)
	if _, err := m.SaveImage(t.Context(), "web", "myimg"); err != nil {
		t.Fatal(err)
	}
	made, err := m.Create(t.Context(), "web2", "myimg", machines.DefaultIdle)
	if err != nil {
		t.Fatal(err)
	}
	req := g.requests[made.ID][0]
	if req.Cmd != "sh" || req.User != "root" || len(req.Args) != 5 ||
		req.Args[0] != "-c" || req.Args[2] != "hibd-reset" ||
		req.Args[3] != "/etc/hibernaut/ssh" || req.Args[4] != made.ID {
		t.Errorf("copy reset isn't a locked script with directory and stable machine ID arguments")
	}
}
