package main

// One SSH session channel: a shell or a command as the remote user, on a
// PTY when the client asks for one. The semantics are sshd's as far as a
// person's tools notice — a login shell in the repo, `exec` in $HOME (what
// scp -O, rsync and VS Code's install script expect), exit status back,
// signals through, the window resized — and no more: no subsystem (sftp is
// a documented limit), no agent forwarding.

import (
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// The request payloads of RFC 4254 §6, as x/crypto marshals them.
type ptyReq struct {
	Term          string
	Cols, Rows    uint32
	Width, Height uint32
	Modes         string
}
type envReq struct{ Name, Value string }
type execReq struct{ Command string }
type winchReq struct{ Cols, Rows, Width, Height uint32 }
type signalReq struct{ Signal string }
type exitStatusMsg struct{ Status uint32 }
type exitSignalMsg struct {
	Signal     string
	CoreDumped bool
	Error      string
	Lang       string
}

// sshEnvAllowed is what a client may set in the session's environment: the
// terminal and locale words OpenSSH's SendEnv carries, nothing a process
// could be tricked by.
var sshEnvAllowed = regexp.MustCompile(`^(TERM|LANG|LANGUAGE|LC_[A-Z_]+|COLORTERM|TZ)$`)

// sshSignals maps the SSH signal names to the signals the kernel knows.
var sshSignals = map[string]syscall.Signal{
	"ABRT": syscall.SIGABRT, "ALRM": syscall.SIGALRM, "HUP": syscall.SIGHUP, "INT": syscall.SIGINT,
	"KILL": syscall.SIGKILL, "PIPE": syscall.SIGPIPE, "QUIT": syscall.SIGQUIT, "TERM": syscall.SIGTERM,
	"USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2,
}

// shellPath is the user's shell: $SHELL, else bash when the image has it,
// else /bin/sh. The terminal and the SSH sessions agree on it.
func shellPath() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	if _, err := exec.LookPath("bash"); err == nil {
		return "bash"
	}
	return "/bin/sh"
}

// sshSession is one session channel's state.
type sshSession struct {
	ch     ssh.Channel
	repo   string
	pty    *ptyReq
	env    []string
	cmd    *exec.Cmd
	master *os.File // the PTY master, when one was requested
	done   chan struct{}
	grace  time.Duration // sshServer.hangupGrace
}

func (s *sshServer) session(nc ssh.NewChannel) {
	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	ss := &sshSession{ch: ch, repo: s.repo, done: make(chan struct{}), grace: s.hangupGrace}
	defer ss.finish()
	for req := range reqs {
		ss.request(req)
	}
}

// request answers one channel request. Only one shell or exec runs per
// session; a pty and environment words are taken before it starts.
func (ss *sshSession) request(req *ssh.Request) {
	ok := false
	switch req.Type {
	case "pty-req":
		var p ptyReq
		if ss.cmd == nil && ssh.Unmarshal(req.Payload, &p) == nil {
			ss.pty = &p
			ok = true
		}
	case "env":
		var e envReq
		if ss.cmd == nil && ssh.Unmarshal(req.Payload, &e) == nil && sshEnvAllowed.MatchString(e.Name) && !strings.ContainsAny(e.Value, "\x00") {
			ss.env = append(ss.env, e.Name+"="+e.Value)
			ok = true
		}
	case "shell":
		ok = ss.cmd == nil && ss.start([]string{shellPath(), "-l"}, ss.repo) == nil
	case "exec":
		var e execReq
		if ss.cmd == nil && ssh.Unmarshal(req.Payload, &e) == nil {
			home, _ := os.UserHomeDir()
			if home == "" {
				home = ss.repo
			}
			ok = ss.start([]string{shellPath(), "-c", e.Command}, home) == nil
		}
	case "window-change":
		var w winchReq
		if ss.master != nil && ssh.Unmarshal(req.Payload, &w) == nil {
			ok = resizePTY(ss.master, int(w.Cols), int(w.Rows)) == nil
		}
	case "signal":
		var sg signalReq
		if sig, known := sshSignals[sigName(req.Payload, &sg)]; known && ss.cmd != nil && ss.cmd.Process != nil {
			ok = syscall.Kill(-ss.cmd.Process.Pid, sig) == nil
		}
	case "subsystem":
		// sftp and the like: not served (documented).
	}
	if req.WantReply {
		_ = req.Reply(ok, nil)
	}
}

func sigName(payload []byte, sg *signalReq) string {
	if ssh.Unmarshal(payload, sg) != nil {
		return ""
	}
	return sg.Signal
}

// start runs argv in dir, on a PTY when one was requested, and wires the
// channel to it. It returns once the process is running; a goroutine
// reports its exit on the channel and closes it.
func (ss *sshSession) start(argv []string, dir string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), ss.env...)
	var wg sync.WaitGroup
	if ss.pty != nil {
		term := ss.pty.Term
		if term == "" {
			term = "xterm-256color"
		}
		cmd.Env = append(cmd.Env, "TERM="+term, "COLORTERM=truecolor")
		master, err := startPTY(cmd, int(ss.pty.Cols), int(ss.pty.Rows))
		if err != nil {
			return err
		}
		ss.master = master
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = io.Copy(ss.ch, master) // ends with EIO when the slave side closes
		}()
		go func() {
			_, _ = io.Copy(master, ss.ch)
			// The client closed its side: hang up on the shell.
			if cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGHUP)
			}
		}()
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return err
		}
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() {
			_, _ = io.Copy(stdin, ss.ch)
			_ = stdin.Close() // the client's EOF is the command's EOF
		}()
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = io.Copy(ss.ch, stdout) }()
		go func() { defer wg.Done(); _, _ = io.Copy(ss.ch.Stderr(), stderr) }()
	}
	ss.cmd = cmd
	go func() {
		// Output first: every byte the process wrote is on the channel before
		// its exit is, and Wait closes the pipes on whatever a slow client
		// has not taken yet.
		wg.Wait()
		err := cmd.Wait()
		ss.reportExit(err)
		_ = ss.ch.Close()
		close(ss.done)
	}()
	return nil
}

// reportExit sends the process's exit on the channel, as sshd does: an
// exit-status, or an exit-signal when a signal ended it.
func (ss *sshSession) reportExit(err error) {
	status := uint32(0)
	if err != nil {
		status = 1
		if ee, ok := err.(*exec.ExitError); ok {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				name := strings.TrimPrefix(ws.Signal().String(), "signal: ")
				for n, sig := range sshSignals {
					if sig == ws.Signal() {
						name = n
					}
				}
				_, _ = ss.ch.SendRequest("exit-signal", false, ssh.Marshal(exitSignalMsg{Signal: name, CoreDumped: ws.CoreDump()}))
				return
			}
			status = uint32(ee.ExitCode())
		}
	}
	_, _ = ss.ch.SendRequest("exit-status", false, ssh.Marshal(exitStatusMsg{Status: status}))
}

// finish runs when the channel's requests end — the client closed it, or
// the process exited and we did: a process still running is told to go,
// then made to.
func (ss *sshSession) finish() {
	if ss.cmd == nil || ss.cmd.Process == nil {
		_ = ss.ch.Close()
		return
	}
	select {
	case <-ss.done:
	default:
		sig := syscall.SIGTERM
		if ss.master != nil {
			sig = syscall.SIGHUP
		}
		_ = syscall.Kill(-ss.cmd.Process.Pid, sig)
		select {
		case <-ss.done:
		case <-time.After(ss.grace):
			_ = syscall.Kill(-ss.cmd.Process.Pid, syscall.SIGKILL)
			<-ss.done
		}
	}
	if ss.master != nil {
		_ = ss.master.Close()
	}
}
