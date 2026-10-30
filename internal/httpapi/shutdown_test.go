package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/engine"
	"apothecary/internal/ports/agentport"
)

// Shutting the server down ends its long-lived responses — a run's event
// stream, an ask_human hold — instead of waiting them out: a graceful
// shutdown that waits for a browser tab's event stream waits for its
// deadline every time.
func TestShutdownEndsLongLivedResponses(t *testing.T) {
	x := newEnv(t, engine.Config{})
	block := make(chan struct{})
	x.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		<-block
		return agentport.AgentResult{TurnsUsed: 1}, nil
	}
	runID := x.createTicket(t, "T")
	defer close(block)
	var token string
	for deadline := time.Now().Add(5 * time.Second); token == "" && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		token = x.e.RunToken(runID)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: x.srv.Handler()}
	srv.RegisterOnShutdown(x.srv.Drain)
	go func() { _ = srv.Serve(ln) }()
	base := "http://" + ln.Addr().String()

	// A browser tab following the run.
	req, _ := http.NewRequest("GET", base+"/api/runs/"+runID+"/events", nil)
	stream, err := http.DefaultClient.Do(x.authed(req))
	if err != nil || stream.StatusCode != http.StatusOK {
		t.Fatalf("stream = %v, %v", stream, err)
	}
	defer stream.Body.Close()
	streamDone := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, stream.Body); streamDone <- err }()

	// The agent's ask_human, holding for an answer.
	body, _ := json.Marshal(map[string]any{"type": "clarification", "question": "which?", "hold_ms": 50000})
	hreq, _ := http.NewRequest("POST", base+"/api/runs/"+runID+"/pings", bytes.NewReader(body))
	hreq.Header.Set("Authorization", "Bearer "+token)
	hreq.Header.Set("Content-Type", "application/json")
	holdDone := make(chan int, 1)
	go func() {
		resp, err := http.DefaultClient.Do(hreq)
		if err != nil {
			holdDone <- -1
			return
		}
		resp.Body.Close()
		holdDone <- resp.StatusCode
	}()
	for deadline := time.Now().Add(5 * time.Second); len(x.e.PendingPings()) == 0 && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
	}
	if len(x.e.PendingPings()) == 0 {
		t.Fatal("the ping was never raised")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown waited out its deadline: %v", err)
	}
	select {
	case <-streamDone:
	case <-time.After(time.Second):
		t.Error("the event stream is still open after shutdown")
	}
	select {
	case code := <-holdDone:
		if code != http.StatusNoContent {
			t.Errorf("the hold ended with %d, want 204 (re-poll)", code)
		}
	case <-time.After(time.Second):
		t.Error("the ping hold is still open after shutdown")
	}
}
