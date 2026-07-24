package main

// portscan discovers listening TCP ports inside the workspace container and
// reports them to the engine (the run page lists them with click-to-expose).
// Pure /proc/net/tcp{,6} parsing — no netstat/ss assumptions, works in any
// image. Reports ride the same loopback→engine bridge and run-token auth as
// ask-human; credentials are re-read from run.env on 401 (engine restarts
// rotate tokens) and the scanner tolerates run.env not existing yet (it is
// spawned at provision time, before bootstrap writes the file).
//
// Deliberately NOT filtered: well-known service ports (postgres, mysql, …) —
// the engine's portrelay is raw TCP, so exposing a discovered db is a
// feature, not a mistake. Filtered: ports < 9, the bridge's own relay
// listeners (--exclude), non-loopback/non-wildcard binds.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Scan knobs — vars so tests can compress time and point at fixtures.
var (
	procTCPPaths      = []string{"/proc/net/tcp", "/proc/net/tcp6"}
	procRoot          = "/proc"
	scanInterval      = 3 * time.Second
	heartbeatInterval = 30 * time.Second
	maxReportPorts    = 128
	// singletonAddr is an abstract unix socket (Linux): held for the process
	// lifetime, dies with the container — the relay's EADDRINUSE idempotence,
	// without a stale file to clean up.
	singletonAddr = "@apothecary-portscan"
)

type observedPort struct {
	Port    int    `json:"port"`
	Process string `json:"process,omitempty"`
}

func runPortScan(args []string) {
	exclude, err := parseExclude(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "portscan: %v\n", err)
		os.Exit(2)
	}
	ln, err := net.Listen("unix", singletonAddr) // "@" = abstract namespace
	if err != nil {
		os.Exit(0) // a previous portscan already runs; let it serve
	}
	defer ln.Close()
	scanLoop(context.Background(), exclude)
}

// parseExclude handles "--exclude 8080,9090" (repeatable).
func parseExclude(args []string) (map[int]bool, error) {
	exclude := map[int]bool{}
	for i := 0; i < len(args); i++ {
		if args[i] != "--exclude" {
			return nil, fmt.Errorf("unknown arg %q (want --exclude <ports>)", args[i])
		}
		i++
		if i >= len(args) {
			return nil, errors.New("--exclude needs a comma-separated port list")
		}
		for p := range strings.SplitSeq(args[i], ",") {
			n, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil || n < 1 || n > 65535 {
				return nil, fmt.Errorf("bad exclude port %q", p)
			}
			exclude[n] = true
		}
	}
	return exclude, nil
}

// scanLoop scans every scanInterval and POSTs the snapshot on change, or
// after heartbeatInterval since the last successful report (the engine ages
// silent runs out of the UI on a TTL, so the heartbeat is what keeps a
// steady-state listener visible). Never gives up: this is a daemon, not a
// tool call — network errors back off to backoffCap and keep trying.
func scanLoop(ctx context.Context, exclude map[int]bool) {
	var lastSent []observedPort
	var lastSentAt time.Time
	sentOnce := false
	backoff := backoffBase

	for {
		if ctx.Err() != nil {
			return
		}
		ports := scanListeners(exclude)
		due := !sentOnce || !slices.Equal(ports, lastSent) ||
			time.Since(lastSentAt) >= heartbeatInterval
		if due {
			env := readRunEnv()
			res, err := callEngine("POST", "/api/runs/"+env["APOTHECARY_RUN_ID"]+"/observed-ports",
				map[string]any{"ports": ports}, env)
			switch {
			case err != nil:
				// Engine (or run.env) not there yet — back off, keep scanning.
				time.Sleep(backoff)
				backoff = min(backoff*2, backoffCap)
				continue
			case res.StatusCode == http.StatusUnauthorized:
				// Rotated token; re-read next round (readRunEnv is per-report).
				drain(res)
			case res.StatusCode/100 == 2:
				drain(res)
				lastSent, lastSentAt, sentOnce = ports, time.Now(), true
			default:
				drain(res) // engine unhappy (e.g. run finished): retry on heartbeat
				lastSent, lastSentAt, sentOnce = ports, time.Now(), true
			}
			backoff = backoffBase
		}
		time.Sleep(scanInterval)
	}
}

// scanListeners returns the filtered, sorted set of LISTEN sockets on
// loopback/wildcard addresses, with best-effort process names.
func scanListeners(exclude map[int]bool) []observedPort {
	inodeByPort := map[int]string{}
	for _, p := range procTCPPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for port, inode := range parseProcNet(string(data)) {
			if port < 9 || exclude[port] {
				continue
			}
			if _, ok := inodeByPort[port]; !ok { // dedupe v4/v6 by port
				inodeByPort[port] = inode
			}
		}
	}
	names := processNames(inodeByPort)
	ports := make([]observedPort, 0, len(inodeByPort))
	for port := range inodeByPort {
		ports = append(ports, observedPort{Port: port, Process: names[port]})
	}
	slices.SortFunc(ports, func(a, b observedPort) int { return a.Port - b.Port })
	if len(ports) > maxReportPorts {
		ports = ports[:maxReportPorts]
	}
	return ports
}

// parseProcNet extracts port→inode for LISTEN (st=0A) rows of a
// /proc/net/tcp{,6} table whose local address is loopback or wildcard.
// Row shape: "sl local_address rem_address st ... uid timeout inode ..."
// with local_address as HEX_IP:HEX_PORT (8 hex chars v4, 32 v6,
// little-endian 32-bit groups).
func parseProcNet(table string) map[int]string {
	out := map[int]string{}
	lines := strings.Split(table, "\n")
	for _, line := range lines[1:] { // skip header
		f := strings.Fields(line)
		if len(f) < 10 || f[3] != "0A" {
			continue
		}
		hexIP, hexPort, ok := strings.Cut(f[1], ":")
		if !ok || !localOrWildcard(hexIP) {
			continue
		}
		port, err := strconv.ParseInt(hexPort, 16, 32)
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		out[int(port)] = f[9]
	}
	return out
}

// localOrWildcard reports whether a /proc/net hex address is loopback or a
// wildcard bind. v4: 0100007F (127.0.0.1) / 00000000. v6: ::1 encodes as
// 0000000000000000000000000...01000000 (last group little-endian), :: as all
// zeros; v4-mapped loopback ends in 0100007F.
func localOrWildcard(hexIP string) bool {
	switch len(hexIP) {
	case 8:
		return hexIP == "0100007F" || hexIP == "00000000"
	case 32:
		head, tail := hexIP[:24], hexIP[24:]
		if tail == "01000000" && head == strings.Repeat("0", 24) {
			return true // ::1
		}
		if hexIP == strings.Repeat("0", 32) {
			return true // ::
		}
		// v4-mapped ::ffff:127.0.0.1 / ::ffff:0.0.0.0
		if strings.HasSuffix(head, "FFFF0000") || strings.HasSuffix(head, "0000FFFF") {
			return tail == "0100007F" || tail == "00000000"
		}
		return false
	}
	return false
}

// processNames best-effort maps ports to process comm names by matching the
// socket inode against /proc/*/fd/* readlinks. Every failure is skipped
// silently — cross-uid /proc entries are routinely unreadable and that is
// fine; the port still reports, just without a name.
func processNames(inodeByPort map[int]string) map[int]string {
	wanted := map[string]int{} // "socket:[inode]" -> port
	for port, inode := range inodeByPort {
		wanted["socket:["+inode+"]"] = port
	}
	names := map[int]string{}
	procs, err := os.ReadDir(procRoot)
	if err != nil {
		return names
	}
	for _, p := range procs {
		if len(names) == len(inodeByPort) {
			break
		}
		pid := p.Name()
		if !p.IsDir() || pid[0] < '0' || pid[0] > '9' {
			continue
		}
		fdDir := filepath.Join(procRoot, pid, "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			port, ok := wanted[link]
			if !ok || names[port] != "" {
				continue
			}
			comm, err := os.ReadFile(filepath.Join(procRoot, pid, "comm"))
			if err != nil {
				continue
			}
			names[port] = strings.TrimSpace(string(comm))
		}
	}
	return names
}
