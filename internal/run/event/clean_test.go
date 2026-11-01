package event

import (
	"reflect"
	"testing"
)

func TestCleanText(t *testing.T) {
	for in, want := range map[string]string{
		"":           "",
		"plain":      "plain",
		"héllo ✓":    "héllo ✓",
		"a\x00b":     "a�b",
		"\x00\x00":   "��",
		"a\xffb":     "a�b",
		"a\xff\x00b": "a��b",
		"kept �":     "kept �",
		`a\u0000b`:   `a\u0000b`, // text that only looks like one
	} {
		if got := CleanText(in); got != want {
			t.Errorf("CleanText(%q) = %q, want %q", in, got, want)
		}
	}
}

// Everything a payload can hold text in, however deep: the walk is what keeps a
// field added later from stranding a run.
func TestCleanReachesEveryField(t *testing.T) {
	type form struct {
		Label string
		Tags  []string
		next  string // unexported: left alone, and no panic
	}
	type payload struct {
		Unknown
		Title   string
		Ptr     *string
		Nested  form
		Forms   []form
		ByName  map[string]string
		Content map[string]any
		Any     any
		Raw     []byte
		Count   int
		None    *string
		NoMap   map[string]string
		NoList  []string
	}
	ptr := "p\x00"
	p := &payload{
		Title: "t\x00", Ptr: &ptr,
		Nested: form{Label: "l\x00", Tags: []string{"a\xff"}, next: "n\x00"},
		Forms:  []form{{Label: "f\x00"}},
		ByName: map[string]string{"k\x00": "v\x00"},
		Content: map[string]any{
			"s": "c\x00", "n": 1.5, "b": true, "z": nil,
			"list": []any{"i\x00", map[string]any{"deep\x00": "d\x00"}},
		},
		Any: "a\x00", Raw: []byte("r\x00"), Count: 3,
	}
	clean := func(ps ...any) {
		for _, x := range ps {
			reflect.ValueOf(x).Elem().Set(cleaned(reflect.ValueOf(x).Elem()))
		}
	}
	clean(p)
	r := "�"
	want := map[string]any{
		"s": "c" + r, "n": 1.5, "b": true, "z": nil,
		"list": []any{"i" + r, map[string]any{"deep" + r: "d" + r}},
	}
	if p.Title != "t"+r || *p.Ptr != "p"+r || p.Nested.Label != "l"+r || p.Nested.Tags[0] != "a"+r || p.Forms[0].Label != "f"+r {
		t.Errorf("fields = %+v", p)
	}
	if p.ByName["k"+r] != "v"+r || len(p.ByName) != 1 {
		t.Errorf("map = %q", p.ByName)
	}
	if !reflect.DeepEqual(p.Content, want) {
		t.Errorf("content = %#v", p.Content)
	}
	if p.Any != "a"+r || p.Count != 3 {
		t.Errorf("any = %v, count = %d", p.Any, p.Count)
	}
	if string(p.Raw) != "r\x00" {
		t.Errorf("bytes are not text: %q", p.Raw)
	}
	if p.Nested.next != "n\x00" {
		t.Errorf("unexported = %q", p.Nested.next)
	}
	if p.None != nil || p.NoMap != nil || p.NoList != nil {
		t.Errorf("nil became empty: %+v", p)
	}
}

// The real entry: in place, so what the caller holds is what was stored, and
// nothing to do for a nil.
func TestCleanPayloads(t *testing.T) {
	failed := &StageFailed{StageID: "s", Error: "boom\x00"}
	Clean(failed, nil, (*RunArchived)(nil))
	if failed.Error != "boom�" {
		t.Errorf("error = %q", failed.Error)
	}
}
