package run

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/run/event"
)

func TestReaderViewAndAll(t *testing.T) {
	ctx := context.Background()
	st := event.NewMemStore()
	if _, err := st.Append(ctx, "r1", &event.RunCreated{ProjectID: "p"}); err != nil {
		t.Fatal(err)
	}
	r := &Reader{Store: st}
	v, err := r.View(ctx, "r1")
	if err != nil || v.RunID != "r1" || v.ProjectID != "p" {
		t.Fatalf("View = %+v, %v", v, err)
	}
	if _, err := r.View(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("View(missing) err = %v", err)
	}
	all, err := r.All(ctx)
	if err != nil || len(all) != 1 || all[0].RunID != "r1" {
		t.Fatalf("All = %+v, %v", all, err)
	}
}
