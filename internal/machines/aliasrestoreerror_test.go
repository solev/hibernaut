package machines_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
)

func TestSaveFinalRestoreFailureNamesSurvivingImage(t *testing.T) {
	v := newEnv(t, fake.WithAliasCache())
	v.create(t, "web")
	if err := v.m.Pause(t.Context(), "web"); err != nil {
		t.Fatal(err)
	}
	e := &faultEngine{Engine: v.e, pause: func(context.Context, string) error {
		return errors.New("restoring pause failed")
	}}
	wrapped := deletionEngine{Engine: e, delete: func(context.Context, string) error {
		return errors.New("delete refused")
	}}
	m := imageManager(v, func(cfg *machines.Config) { cfg.Engine = wrapped })
	img, err := m.SaveImage(t.Context(), "web", "myimg")
	if img.Name != "myimg" || err == nil {
		t.Fatalf("SaveImage = %+v, %v, want surviving image and error", img, err)
	}
	for _, want := range []string{"removal is unconfirmed", "Wait a few minutes", "hib image rm myimg"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("SaveImage = %v, want %q", err, want)
		}
	}
}
