package images

import (
	"testing"
	"time"
)

func TestTemplateKeepsSavedImagesApart(t *testing.T) {
	for name, want := range map[string]string{
		"dev":   "hib-dev",
		"myimg": "hibimg-myimg",
	} {
		if got := Template(name); got != want {
			t.Errorf("Template(%s) = %s, want %s", name, got, want)
		}
	}
	if got := SavedTemplate("dev"); got != "hibimg-dev" {
		t.Errorf("SavedTemplate(dev) = %s, want hibimg-dev: a saved image a built-in one shadows", got)
	}
}

func TestSavedName(t *testing.T) {
	for alias, want := range map[string]string{
		"hibimg-myimg":         "myimg",
		"hibimg-":              "",
		"hibimg-Bad_name":      "",
		"hib-dev":              "",
		"hibfork-abc-17596000": "",
	} {
		if got, ok := SavedName(alias); got != want || ok != (want != "") {
			t.Errorf("SavedName(%s) = %q, %v, want %q", alias, got, ok, want)
		}
	}
}

func TestForkTemplate(t *testing.T) {
	alias := ForkTemplate("i8k2w3d0m9z1q7x5c4v6", time.UnixMilli(1759600000123))
	if alias != "hibfork-i8k2w3d0m9z1q7x5c4v6-1759600000123" {
		t.Errorf("ForkTemplate = %s", alias)
	}
	if !IsForkTemplate(alias) || IsForkTemplate("hibimg-hibfork") || IsForkTemplate("hib-dev") {
		t.Error("IsForkTemplate doesn't tell fork templates from the others")
	}
}
