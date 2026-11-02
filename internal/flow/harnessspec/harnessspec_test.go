package harnessspec

import "testing"

func TestRegistryIsConsistent(t *testing.T) {
	kinds := map[string]bool{}
	for _, h := range Registry() {
		if kinds[h.Kind] {
			t.Errorf("harness %q registered twice", h.Kind)
		}
		kinds[h.Kind] = true
		names := map[string]bool{}
		for _, o := range h.Options {
			if names[o.Name] {
				t.Errorf("%s: option %q registered twice", h.Kind, o.Name)
			}
			names[o.Name] = true
			switch o.Kind {
			case KindModel:
			case KindEnum:
				if len(o.Values) == 0 {
					t.Errorf("%s.%s: an enum option needs values", h.Kind, o.Name)
				}
			default:
				t.Errorf("%s.%s: unknown option kind %q", h.Kind, o.Name, o.Kind)
			}
		}
		for _, o := range h.Options {
			if o.Needs != "" && (o.Needs == o.Name || !names[o.Needs]) {
				t.Errorf("%s.%s needs %q, which is not another option of %s", h.Kind, o.Name, o.Needs, h.Kind)
			}
		}
	}
}
