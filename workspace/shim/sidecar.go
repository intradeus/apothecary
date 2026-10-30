package main

// shim sidecar — the per-workspace runtime the engine drives (one per
// workspace, started on demand by the provisioner, singleton by socket).
//
// It serves HTTP on a unix socket in the bind-mounted shim dir, so the engine
// dials the same file host-side with no container networking involved (the
// same channel the engine bridge and the port relays use):
//
//	POST /agent/start     launch (or reuse) the ACP agent process + initialize
//	POST /agent/session   new or load a session; apply mode (agent) and model
//	POST /agent/prompt    start a turn (async; ends with a turn_end event)
//	POST /agent/cancel    cancel the session's turn in flight
//	POST /agent/stop      kill the agent process
//	GET  /agent/status    boot id, live sessions + costs, the turn in flight
//	GET  /agent/events    NDJSON: buffered events after ?after=, then live
//	GET  /fs/tree         one directory level (?path=)
//	GET  /fs/file         one file's content (?path=)
//	GET  /git/changes     the run's changes against ?base= (merge-base)
//	GET  /git/diff        one file's unified diff (?base=&path=)
//	GET  /term            WebSocket terminal (PTY) in the repo
//	GET  /services        every service's state (shim service status)
//	GET  /services/{name}/log  the last ?tail= lines of its output
//
// Beside the HTTP socket it serves SSH on ssh.sock (sshd.go): the far end of
// the engine's SSH door — a person's ssh or VS Code Remote-SSH session,
// shell and TCP forwards, as the remote user — authenticated by the door's
// key the engine writes to /apothecary/ssh_door.pub.
//
// Every workspace route takes ?repo=<name> to address one of a multi-repo
// run's additional checkouts (~/repos/<name>); absent = the primary.
//
// The agent process lives as long as the sidecar does, which is as long as
// the container: an engine restart neither kills a turn in flight nor loses
// its events.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type sidecar struct {
	repo string
	// repos holds a multi-repo run's additional checkouts (~/repos/<name>);
	// a request's ?repo= names one of them, "" or "repo" the primary.
	repos string
	log   *eventLog
	agent *agentHost
}

// errNoSuchRepo: ?repo= names a checkout this workspace does not have.
var errNoSuchRepo = errors.New("no such repository checkout")

// root resolves a request's ?repo= to a checkout directory.
func (s *sidecar) root(r *http.Request) (string, error) {
	name := r.URL.Query().Get("repo")
	if name == "" || name == "repo" {
		return s.repo, nil
	}
	if s.repos == "" || !repoNameRe.MatchString(name) {
		return "", errNoSuchRepo
	}
	dir := filepath.Join(s.repos, name)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", errNoSuchRepo
	}
	return dir, nil
}

var repoNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func runSidecar(args []string) {
	fs := flag.NewFlagSet("sidecar", flag.ExitOnError)
	listen := fs.String("listen", "/opt/apothecary/sidecar.sock", "unix socket to serve on")
	repo := fs.String("repo", "", "repository root (default $HOME/repo)")
	repos := fs.String("repos", "", "directory of additional checkouts (default $HOME/repos)")
	daemon := fs.Bool("daemon", false, "detach into the background and return once serving")
	logPath := fs.String("log", "/apothecary/sidecar.log", "log file when detached")
	sshSock := fs.String("ssh", "", "unix socket for the SSH server (default: ssh.sock beside --listen; off disables)")
	_ = fs.Parse(args)
	if *sshSock == "" {
		*sshSock = filepath.Join(filepath.Dir(*listen), "ssh.sock")
	}
	if *repo == "" || *repos == "" {
		home, _ := os.UserHomeDir()
		if *repo == "" {
			*repo = filepath.Join(home, "repo")
		}
		if *repos == "" {
			*repos = filepath.Join(home, "repos")
		}
	}
	// Singleton: a live socket means a sidecar is already serving.
	if c, err := net.DialTimeout("unix", *listen, time.Second); err == nil {
		c.Close()
		return
	}
	if *daemon {
		os.Exit(detach(*listen, *repo, *repos, *logPath, *sshSock))
	}
	_ = os.Remove(*listen)
	ln, err := net.Listen("unix", *listen)
	if err != nil {
		log.Fatalf("sidecar: listen %s: %v", *listen, err)
	}
	// The engine dials this host-side, usually as another uid.
	_ = os.Chmod(*listen, 0o666)
	if *sshSock != "off" {
		// The SSH server shares the sidecar's life; it is never what decides
		// whether a sidecar is serving (the HTTP socket is), so a failure
		// here is logged, not fatal.
		if _, err := startSSH(*sshSock, *repo); err != nil {
			log.Printf("sidecar: ssh: not serving on %s: %v", *sshSock, err)
		} else {
			log.Printf("sidecar: ssh on %s", *sshSock)
		}
	}
	evl := newEventLog()
	s := &sidecar{repo: *repo, repos: *repos, log: evl, agent: newAgentHost(evl)}
	log.Printf("sidecar: serving on %s (repo %s, boot %s)", *listen, *repo, evl.boot)
	srv := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.Serve(ln))
}

// detach re-execs the sidecar in its own session with output to a log file,
// then waits for the socket so the caller knows it is serving. Uses no
// setsid/nohup binaries: the workspace image is the repo's own.
func detach(listen, repo, repos, logPath, sshSock string) int {
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		lf, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	}
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	cmd := exec.Command(self, "sidecar", "--listen", listen, "--repo", repo, "--repos", repos, "--ssh", sshSock)
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "sidecar: start: %v\n", err)
		return 1
	}
	_ = cmd.Process.Release()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("unix", listen, time.Second); err == nil {
			c.Close()
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "sidecar: not serving on %s after 15s (see %s)\n", listen, logPath)
	return 1
}

func (s *sidecar) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		// repo is the checkout as the sidecar was told it; workspace the
		// directory it resolves to — the devcontainer's workspace folder,
		// which $HOME/repo is a symlink onto. What an editor opens.
		ws, err := filepath.EvalSymlinks(s.repo)
		if err != nil {
			ws = ""
		}
		writeJSONResp(w, 200, map[string]string{"boot": s.log.boot, "repo": s.repo, "workspace": ws})
	})
	mux.HandleFunc("POST /agent/start", s.agentStart)
	mux.HandleFunc("POST /agent/session", s.agentSession)
	mux.HandleFunc("POST /agent/prompt", s.agentPrompt)
	mux.HandleFunc("POST /agent/cancel", s.agentCancel)
	mux.HandleFunc("POST /agent/stop", func(w http.ResponseWriter, r *http.Request) {
		s.agent.stop()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /agent/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(w, 200, s.agent.status())
	})
	mux.HandleFunc("GET /agent/events", s.agentEvents)
	mux.HandleFunc("GET /fs/tree", s.fsTree)
	mux.HandleFunc("GET /fs/file", s.fsFile)
	mux.HandleFunc("GET /git/changes", s.gitChanges)
	mux.HandleFunc("GET /git/diff", s.gitDiff)
	mux.HandleFunc("GET /term", s.terminal)
	mux.HandleFunc("GET /services", serveServiceStatus)
	mux.HandleFunc("GET /services/{name}/log", serveServiceLog)
	return mux
}

func writeJSONResp(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErrResp(w http.ResponseWriter, code int, msg string) {
	writeJSONResp(w, code, map[string]string{"error": msg})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 32<<20)).Decode(v); err != nil {
		writeErrResp(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return false
	}
	return true
}

func (s *sidecar) agentStart(w http.ResponseWriter, r *http.Request) {
	var req startReq
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Cwd == "" {
		req.Cwd = s.repo
	}
	p, err := s.agent.start(req)
	if err != nil {
		writeErrResp(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSONResp(w, 200, map[string]any{"boot": s.log.boot, "init": p.init, "proc": p.id})
}

func (s *sidecar) agentSession(w http.ResponseWriter, r *http.Request) {
	var req sessionReq
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Cwd == "" {
		req.Cwd = s.repo
	}
	resp, err := s.agent.session(req)
	switch {
	case errors.Is(err, errAgentGone):
		writeErrResp(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErrResp(w, http.StatusBadGateway, err.Error())
	default:
		writeJSONResp(w, 200, resp)
	}
}

func (s *sidecar) agentPrompt(w http.ResponseWriter, r *http.Request) {
	var req promptReq
	if !decodeBody(w, r, &req) {
		return
	}
	switch err := s.agent.prompt(req); {
	case errors.Is(err, errBusy), errors.Is(err, errAgentGone):
		writeErrResp(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErrResp(w, http.StatusBadRequest, err.Error())
	default:
		writeJSONResp(w, http.StatusAccepted, map[string]int64{"seq": s.log.last()})
	}
}

func (s *sidecar) agentCancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.agent.cancel(req.SessionID); err != nil {
		writeErrResp(w, http.StatusConflict, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// agentEvents streams NDJSON. ?after= is the last seq the caller has; ?boot=
// the sidecar instance it was read from — a different boot means this is a
// new sidecar whose seqs restarted, and the caller is told so with a gap.
func (s *sidecar) agentEvents(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErrResp(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(200)
	enc := json.NewEncoder(w)
	if boot := r.URL.Query().Get("boot"); boot != "" && boot != s.log.boot {
		_ = enc.Encode(sideEvent{Type: "gap", Boot: s.log.boot, Text: "sidecar restarted; earlier events are gone"})
		after = 0
	}
	// follow=0: just what is buffered, then close (a quick look-back).
	follow := r.URL.Query().Get("follow") != "0"
	wake, unsub := s.log.subscribe()
	defer unsub()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	first := true
	for {
		evs, gap := s.log.since(after)
		if gap && first {
			_ = enc.Encode(sideEvent{Type: "gap", Boot: s.log.boot, Text: "events were dropped from the sidecar buffer"})
		}
		first = false
		for _, ev := range evs {
			if err := enc.Encode(ev); err != nil {
				return
			}
			after = ev.Seq
		}
		flusher.Flush()
		if !follow {
			return
		}
		select {
		case <-wake:
		case <-keepalive.C:
			// A blank line: lets the reader notice a dead connection.
			if _, err := w.Write([]byte("\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *sidecar) fsTree(w http.ResponseWriter, r *http.Request) {
	root, err := s.root(r)
	if err != nil {
		writeFSErr(w, err)
		return
	}
	ents, err := listDir(root, r.URL.Query().Get("path"))
	if err != nil {
		writeFSErr(w, err)
		return
	}
	writeJSONResp(w, 200, ents)
}

func (s *sidecar) fsFile(w http.ResponseWriter, r *http.Request) {
	root, err := s.root(r)
	if err != nil {
		writeFSErr(w, err)
		return
	}
	f, err := readRepoFile(root, r.URL.Query().Get("path"))
	if err != nil {
		writeFSErr(w, err)
		return
	}
	writeJSONResp(w, 200, f)
}

func writeFSErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errOutsideRepo):
		writeErrResp(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, errNoSuchRepo):
		writeErrResp(w, http.StatusNotFound, err.Error())
	case errors.Is(err, os.ErrNotExist):
		writeErrResp(w, http.StatusNotFound, "no such file or directory")
	default:
		writeErrResp(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *sidecar) gitChanges(w http.ResponseWriter, r *http.Request) {
	root, err := s.root(r)
	if err != nil {
		writeFSErr(w, err)
		return
	}
	c, err := gitChanges(root, r.URL.Query().Get("base"))
	if err != nil {
		writeErrResp(w, http.StatusConflict, err.Error())
		return
	}
	writeJSONResp(w, 200, c)
}

func (s *sidecar) gitDiff(w http.ResponseWriter, r *http.Request) {
	root, err := s.root(r)
	if err != nil {
		writeFSErr(w, err)
		return
	}
	d, err := gitDiff(root, r.URL.Query().Get("base"), r.URL.Query().Get("path"))
	if err != nil {
		writeFSErr(w, err)
		return
	}
	writeJSONResp(w, 200, d)
}

// terminal runs an interactive shell in the repo on a PTY, over a WebSocket.
// Binary frames carry terminal bytes both ways; a text frame from the client
// is a control message: {"type":"resize","cols":N,"rows":N}.
func (s *sidecar) terminal(w http.ResponseWriter, r *http.Request) {
	root, err := s.root(r)
	if err != nil {
		writeFSErr(w, err)
		return
	}
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	shell := shellPath()
	ws, err := wsAccept(w, r)
	if err != nil {
		return
	}
	defer ws.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-l")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGHUP) }
	master, err := startPTY(cmd, cols, rows)
	if err != nil {
		_ = ws.write(wsOpBinary, []byte("\r\nterminal unavailable: "+err.Error()+"\r\n"))
		return
	}
	defer master.Close()
	go func() {
		_ = cmd.Wait()
		_ = master.Close()
		cancel()
	}()
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				if werr := ws.write(wsOpBinary, buf[:n]); werr != nil {
					cancel()
					return
				}
			}
			if err != nil {
				cancel()
				_ = ws.c.Close()
				return
			}
		}
	}()
	for {
		op, data, err := ws.read()
		if err != nil {
			return
		}
		if op == wsOpText {
			var ctl struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if json.Unmarshal(data, &ctl) == nil && ctl.Type == "resize" {
				_ = resizePTY(master, ctl.Cols, ctl.Rows)
				continue
			}
			if strings.HasPrefix(string(data), "{") {
				continue // an unknown control message is not keystrokes
			}
		}
		if _, err := master.Write(data); err != nil {
			return
		}
	}
}
