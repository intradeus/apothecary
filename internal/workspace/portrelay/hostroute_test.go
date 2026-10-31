package portrelay

import (
	"context"
	"net"
	"testing"
)

type nopConn struct{ closed bool }

func (n *nopConn) Dial(context.Context) (net.Conn, error) { return nil, nil }
func (n *nopConn) Close() error                           { n.closed = true; return nil }

func TestHostRoutedExposure(t *testing.T) {
	m := New("0.0.0.0")
	m.RouteByHost("Apps.Example.com.", "https")
	run := "01HZX4V8Q2M9N7P3R5T1W6Y0AB"
	pc := &nopConn{}
	url, err := m.Expose(run, 3000, pc)
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://3000-r5t1w6y0ab.apps.example.com/" {
		t.Fatalf("url = %q", url)
	}
	if again, _ := m.Expose(run, 3000, &nopConn{}); again != url {
		t.Fatalf("re-expose = %q", again)
	}
	for _, host := range []string{"3000-r5t1w6y0ab.apps.example.com", "3000-R5T1W6Y0AB.apps.example.com:443"} {
		gotRun, port, got, ok := m.Lookup(host)
		if !ok || gotRun != run || port != 3000 || got != pc {
			t.Fatalf("Lookup(%s) = %s %d %v %v", host, gotRun, port, got, ok)
		}
	}
	for _, host := range []string{"apps.example.com", "3001-r5t1w6y0ab.apps.example.com", "3000-r5t1w6y0ab.evil.com",
		"x.3000-r5t1w6y0ab.apps.example.com", "3000-zzzz.apps.example.com"} {
		if _, _, _, ok := m.Lookup(host); ok {
			t.Errorf("Lookup(%s) matched", host)
		}
	}
	if !m.InAppsDomain("anything.apps.example.com") || m.InAppsDomain("apothecary.example.com") {
		t.Error("InAppsDomain")
	}
	if got := m.SavedPorts(run); len(got) != 1 || got[0] != 3000 {
		t.Errorf("SavedPorts = %v", got)
	}
	m.CloseRun(run)
	if !pc.closed {
		t.Error("PortConn not closed with the run")
	}
	if _, _, _, ok := m.Lookup("3000-r5t1w6y0ab.apps.example.com"); ok {
		t.Error("closed run still routed")
	}
}

func TestHostRoutingKeepsTheDomainsPort(t *testing.T) {
	m := New("")
	m.RouteByHost("apps.localhost:8090", "http")
	url, _ := m.Expose("01ABCDEFGHJK", 8471, &nopConn{})
	if url != "http://8471-abcdefghjk.apps.localhost:8090/" {
		t.Fatalf("url = %q", url)
	}
	if _, _, _, ok := m.Lookup("8471-abcdefghjk.apps.localhost:8090"); !ok {
		t.Fatal("lookup with port failed")
	}
}
