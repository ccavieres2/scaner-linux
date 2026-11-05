package main

import (
	"testing"

	"arsop-scanner/internal/config"
)

func TestSelectSources(t *testing.T) {
	on, off := true, false
	cfg := &config.Config{Sources: []config.SourceConfig{
		{ID: "a", Enabled: &on}, {ID: "b", Enabled: &off}, {ID: "c", Enabled: &on},
	}}
	ids := func(l []*config.SourceConfig) (s string) {
		for _, x := range l {
			s += x.ID
		}
		return
	}
	if l, err := selectSources(cfg, nil, true); err != nil || ids(l) != "ac" {
		t.Errorf("--all debe omitir deshabilitadas: %q %v", ids(l), err)
	}
	if l, err := selectSources(cfg, []string{"c", "a", "c"}, false); err != nil || ids(l) != "ca" {
		t.Errorf("orden y duplicados: %q %v", ids(l), err)
	}
	for name, c := range map[string]struct {
		names []string
		all   bool
	}{
		"ninguna":       {nil, false},
		"ambas":         {[]string{"a"}, true},
		"deshabilitada": {[]string{"b"}, false},
		"desconocida":   {[]string{"a", "z"}, false},
	} {
		if _, err := selectSources(cfg, c.names, c.all); err == nil {
			t.Errorf("%s: se esperaba error", name)
		}
	}
}
