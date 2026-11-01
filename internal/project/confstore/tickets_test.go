package confstore

import (
	"context"
	"errors"
	"testing"
)

func TestTicketCRUD(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	p := seedProject(t, svc)

	// Missing project → ErrInvalid, no row.
	if _, err := svc.CreateTicket(ctx, Ticket{ProjectID: "nope", Title: "T"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create with bad project = %v, want ErrInvalid", err)
	}
	// Missing title → ErrInvalid.
	if _, err := svc.CreateTicket(ctx, Ticket{ProjectID: p.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create without title = %v, want ErrInvalid", err)
	}

	tk, err := svc.CreateTicket(ctx, Ticket{ProjectID: p.ID, Title: "Add health", Goal: "g", BaseBranch: "fix/x"})
	if err != nil {
		t.Fatal(err)
	}
	if tk.ID == "" || tk.CreatedAt.IsZero() || tk.UpdatedAt != tk.CreatedAt {
		t.Fatalf("stamps not set: %+v", tk)
	}

	got, err := svc.GetTicket(ctx, tk.ID)
	if err != nil || got.Title != "Add health" || got.BaseBranch != "fix/x" {
		t.Fatalf("get = %+v, %v", got, err)
	}

	list, err := svc.ListTickets(ctx, "")
	if err != nil || len(list) != 1 || list[0].ID != tk.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}

	upd, err := svc.UpdateTicket(ctx, tk.ID, Ticket{ProjectID: p.ID, Title: "Renamed", BaseBranch: "fix/y"})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Title != "Renamed" || upd.BaseBranch != "fix/y" || !upd.CreatedAt.Equal(pgTime(tk.CreatedAt)) {
		t.Fatalf("update = %+v (created_at must be preserved)", upd)
	}

	if err := svc.DeleteTicket(ctx, tk.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetTicket(ctx, tk.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}
