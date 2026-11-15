package confstore

import (
	"context"
	"errors"
	"testing"
)

func TestTicketCRUD(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()

	// No owner → ErrInvalid, no row.
	if _, err := svc.CreateTicket(ctx, Ticket{Title: "T"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create without owner = %v, want ErrInvalid", err)
	}
	// Missing title → ErrInvalid.
	if _, err := svc.CreateTicket(ctx, Ticket{UserID: "u1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create without title = %v, want ErrInvalid", err)
	}

	tk, err := svc.CreateTicket(ctx, Ticket{UserID: "u1", Title: "Add health", Goal: "g", BaseBranch: "fix/x"})
	if err != nil {
		t.Fatal(err)
	}
	if tk.ID == "" || tk.CreatedAt.IsZero() || tk.UpdatedAt != tk.CreatedAt {
		t.Fatalf("stamps not set: %+v", tk)
	}
	in, err := svc.InternalTracker(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc.InternalTickets(ctx, in.ID, InternalTicketQuery{})
	if err != nil || len(list) != 1 || list[0].ID != tk.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}

	got, err := svc.GetTicket(ctx, tk.ID)
	if err != nil || got.Title != "Add health" || got.BaseBranch != "fix/x" {
		t.Fatalf("get = %+v, %v", got, err)
	}

	upd, err := svc.UpdateTicket(ctx, tk.ID, Ticket{Title: "Renamed", BaseBranch: "fix/y"})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Title != "Renamed" || upd.BaseBranch != "fix/y" || !upd.CreatedAt.Equal(pgTime(tk.CreatedAt)) ||
		upd.UserID != "u1" || upd.SourceID != in.ID || upd.RemoteID != tk.ID {
		t.Fatalf("update = %+v (created_at, owner and source must be preserved)", upd)
	}

	if err := svc.DeleteTicket(ctx, tk.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetTicket(ctx, tk.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}
