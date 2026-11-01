package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// newTestSSH serves the sidecar's SSH server on a temp socket with a fresh
// door key written where the server looks, and returns a client over it.
func newTestSSH(t *testing.T) (client *ssh.Client, door ssh.Signer, repo, sock string) {
	t.Helper()
	dir := t.TempDir()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	door, err = ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	old := doorKeyPath
	doorKeyPath = filepath.Join(dir, "ssh_door.pub")
	t.Cleanup(func() { doorKeyPath = old })
	if err := os.WriteFile(doorKeyPath, ssh.MarshalAuthorizedKey(door.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	// A short socket path: unix socket names are capped around 100 bytes.
	sock = filepath.Join(shortTempDir(t), "ssh.sock")
	ln, err := startSSH(sock, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return dialTestSSH(t, sock, door), door, dir, sock
}

func dialTestSSH(t *testing.T, sock string, key ssh.Signer) *ssh.Client {
	t.Helper()
	c, err := ssh.Dial("unix", sock, &ssh.ClientConfig{
		User: "apothecary", Auth: []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// A command runs in the user's shell: its output, its exit status, its
// stdin's end.
func TestSSHExecRunsACommand(t *testing.T) {
	client, _, _, _ := newTestSSH(t)
	t.Setenv("SHELL", "/bin/sh")
	ses, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	ses.Stdin = strings.NewReader("abc")
	var stderr bytes.Buffer
	ses.Stderr = &stderr
	out, err := ses.Output("cat; echo -n err >&2; exit 7")
	var ee *ssh.ExitError
	if !errors.As(err, &ee) || ee.ExitStatus() != 7 {
		t.Fatalf("err = %v (out %q)", err, out)
	}
	if string(out) != "abc" || stderr.String() != "err" {
		t.Errorf("out = %q, stderr = %q", out, stderr.String())
	}
	// A clean exit is status 0.
	ses2, _ := client.NewSession()
	defer ses2.Close()
	if out, err := ses2.Output("echo ok"); err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Errorf("echo ok = %q, %v", out, err)
	}
}

// The environment a client may set is the terminal and locale words; the
// rest is refused, and exec runs in $HOME.
func TestSSHEnvAllowlistAndCwd(t *testing.T) {
	client, _, _, _ := newTestSSH(t)
	t.Setenv("SHELL", "/bin/sh")
	home, _ := os.UserHomeDir()
	ses, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	if err := ses.Setenv("LANG", "fr_FR.UTF-8"); err != nil {
		t.Fatalf("LANG refused: %v", err)
	}
	if err := ses.Setenv("LD_PRELOAD", "/tmp/evil.so"); err == nil {
		t.Fatal("LD_PRELOAD accepted")
	}
	out, err := ses.Output("echo $LANG; echo $LD_PRELOAD; pwd")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 || lines[0] != "fr_FR.UTF-8" || lines[1] != "" || lines[2] != home {
		t.Errorf("out = %q (home %s)", out, home)
	}
}

// direct-tcpip dials inside the workspace: a loopback echo answers.
func TestSSHDirectTCPIP(t *testing.T) {
	client, _, _, _ := newTestSSH(t)
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				buf := make([]byte, 64)
				n, _ := c.Read(buf)
				_, _ = c.Write(bytes.ToUpper(buf[:n]))
				c.Close()
			}()
		}
	}()
	c, err := client.Dial("tcp", echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _ := c.Read(buf)
	if string(buf[:n]) != "PING" {
		t.Errorf("echoed %q", buf[:n])
	}
	// A port nobody listens on is refused, not hung.
	if _, err := client.Dial("tcp", "127.0.0.1:1"); err == nil {
		t.Error("dialing a closed port succeeded")
	}
}

// Only the door's key gets in; no password, no other key, and nothing when
// the engine has not written the key yet.
func TestSSHAcceptsOnlyTheDoorKey(t *testing.T) {
	_, _, _, sock := newTestSSH(t)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherKey, _ := ssh.NewSignerFromKey(other)
	for _, cfg := range []*ssh.ClientConfig{
		{User: "apothecary", Auth: []ssh.AuthMethod{ssh.PublicKeys(otherKey)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second},
		{User: "apothecary", Auth: []ssh.AuthMethod{ssh.Password("secret")}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second},
	} {
		if c, err := ssh.Dial("unix", sock, cfg); err == nil {
			c.Close()
			t.Errorf("auth %v accepted", cfg.Auth)
		}
	}
	// The key file gone: even the door is refused until it is written again.
	saved, _ := os.ReadFile(doorKeyPath)
	_ = os.Remove(doorKeyPath)
	if doorKeyAccepts(mustKey(t, saved)) {
		t.Error("a missing key file accepted a key")
	}
	_ = os.WriteFile(doorKeyPath, saved, 0o600)
	if !doorKeyAccepts(mustKey(t, saved)) {
		t.Error("the door key is refused once the file is back")
	}
}

func mustKey(t *testing.T, line []byte) ssh.PublicKey {
	t.Helper()
	k, _, _, _, err := ssh.ParseAuthorizedKey(line)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// A subsystem (sftp) is refused, and an unknown channel type too.
func TestSSHRefusesWhatItDoesNotServe(t *testing.T) {
	client, _, _, _ := newTestSSH(t)
	ses, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	if err := ses.RequestSubsystem("sftp"); err == nil {
		t.Error("sftp subsystem accepted")
	}
	if _, _, err := client.OpenChannel("x11", nil); err == nil {
		t.Error("an x11 channel opened")
	}
}

// A client slower than the command keeps everything it wrote: the process
// exits with the SSH window full and the last bytes still in the pipe.
func TestSSHExecDeliversAllOutputToASlowClient(t *testing.T) {
	client, _, repo, _ := newTestSSH(t)
	t.Setenv("SHELL", "/bin/sh")
	const size = 2<<20 + 48<<10 // past the 2 MiB window, within the pipe
	exited := filepath.Join(repo, "exited")
	ses, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	stdout, err := ses.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := ses.Start(fmt.Sprintf("head -c %d /dev/zero; touch %s", size, exited)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(exited); return err == nil })
	time.Sleep(50 * time.Millisecond) // long enough for an early Wait to bite
	if n, err := io.Copy(io.Discard, stdout); err != nil || n != size {
		t.Errorf("got %d of %d bytes, %v", n, size, err)
	}
	if err := ses.Wait(); err != nil {
		t.Errorf("exit = %v", err)
	}
}

// startedCommand runs a shell command that first prints its pid, and returns
// the pid once it has.
func startedCommand(t *testing.T, ses *ssh.Session, command string) int {
	t.Helper()
	stdout, err := ses.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := ses.Start("echo $$; " + command); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("pid line %q: %v", line, err)
	}
	return pid
}

// A signal reaches the command's group, and its death comes back as an
// exit-signal; what it is not sure of, it does not send.
func TestSSHSignalReachesTheCommand(t *testing.T) {
	client, _, _, _ := newTestSSH(t)
	t.Setenv("SHELL", "/bin/sh")
	ses, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	pid := startedCommand(t, ses, "exec sleep 60")
	if ok, err := ses.SendRequest("signal", true, ssh.Marshal(signalReq{Signal: "WINCH"})); ok || err != nil {
		t.Errorf("a signal outside the list = %v, %v", ok, err)
	}
	if ok, err := ses.SendRequest("signal", true, []byte{1}); ok || err != nil {
		t.Errorf("a malformed signal = %v, %v", ok, err)
	}
	if !groupAlive(pid) {
		t.Fatal("a refused signal reached the command")
	}
	if err := ses.Signal(ssh.SIGTERM); err != nil {
		t.Fatal(err)
	}
	var ee *ssh.ExitError
	if err := ses.Wait(); !errors.As(err, &ee) || ee.Signal() != "TERM" {
		t.Fatalf("exit = %v, want the TERM signal", err)
	}
}

// A signal SSH has no name for still comes back as what killed the command.
func TestSSHExitSignalOutsideTheList(t *testing.T) {
	client, _, _, _ := newTestSSH(t)
	t.Setenv("SHELL", "/bin/sh")
	ses, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	var ee *ssh.ExitError
	if err := ses.Run("kill -VTALRM $$"); !errors.As(err, &ee) || ee.Signal() == "" {
		t.Fatalf("exit = %v, want a signal", err)
	}
}

// reportExit on an error that is no process's exit: status 1, not silence.
func TestSSHReportExitOnAWaitError(t *testing.T) {
	ch := &recordingChannel{}
	(&sshSession{ch: ch}).reportExit(errors.New("wait failed"))
	var msg exitStatusMsg
	if ch.req != "exit-status" || ssh.Unmarshal(ch.payload, &msg) != nil || msg.Status != 1 {
		t.Errorf("sent %q %v", ch.req, ch.payload)
	}
}

// recordingChannel notes the last request sent on it.
type recordingChannel struct {
	ssh.Channel
	req     string
	payload []byte
}

func (c *recordingChannel) SendRequest(name string, _ bool, payload []byte) (bool, error) {
	c.req, c.payload = name, payload
	return true, nil
}

// A client that hangs up takes its command with it.
func TestSSHHangupStopsTheCommand(t *testing.T) {
	client, _, _, _ := newTestSSH(t)
	t.Setenv("SHELL", "/bin/sh")
	ses, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	pid := startedCommand(t, ses, "exec sleep 60")
	ses.Close()
	waitGone(t, pid)
}

// One that shrugs off the hangup is killed once its grace is up.
func TestSSHHangupKillsWhatIgnoresIt(t *testing.T) {
	_, door, _, _ := newTestSSH(t)
	t.Setenv("SHELL", "/bin/sh")
	s, err := newSSHServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.hangupGrace = 20 * time.Millisecond
	sock := filepath.Join(shortTempDir(t), "ssh.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go s.serve(ln)
	ses, err := dialTestSSH(t, sock, door).NewSession()
	if err != nil {
		t.Fatal(err)
	}
	pid := startedCommand(t, ses, `trap '' TERM HUP; sleep 60 >/dev/null 2>&1`)
	ses.Close()
	waitGone(t, pid)
}

// Before its command a session takes a pty and environment words; after it,
// nothing but the command's own business. Malformed requests are refused.
func TestSSHRequestsOutOfTurn(t *testing.T) {
	client, _, _, _ := newTestSSH(t)
	t.Setenv("SHELL", "/bin/sh")
	ses, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	for _, typ := range []string{"pty-req", "env", "exec"} {
		if ok, err := ses.SendRequest(typ, true, []byte{1}); ok || err != nil {
			t.Errorf("malformed %s = %v, %v", typ, ok, err)
		}
	}
	if ok, _ := ses.SendRequest("window-change", true, ssh.Marshal(winchReq{Cols: 80, Rows: 24})); ok {
		t.Error("a window change with no pty accepted")
	}
	if ok, _ := ses.SendRequest("env", true, ssh.Marshal(envReq{Name: "LANG", Value: "a\x00b"})); ok {
		t.Error("a NUL in an environment word accepted")
	}
	stdin, err := ses.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	startedCommand(t, ses, "cat")
	for typ, payload := range map[string][]byte{
		"pty-req": ssh.Marshal(ptyReq{Term: "xterm", Cols: 80, Rows: 24}),
		"env":     ssh.Marshal(envReq{Name: "LANG", Value: "C"}),
		"exec":    ssh.Marshal(execReq{Command: "true"}),
		"shell":   nil,
	} {
		if ok, err := ses.SendRequest(typ, true, payload); ok || err != nil {
			t.Errorf("%s after the command started = %v, %v", typ, ok, err)
		}
	}
	stdin.Close()
	if err := ses.Wait(); err != nil {
		t.Errorf("exit = %v", err)
	}
}

// exec runs in $HOME, the repo when there is none, and is refused rather
// than hung when it cannot start at all.
func TestSSHExecDirectory(t *testing.T) {
	client, _, repo, _ := newTestSSH(t)
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("HOME", "")
	ses, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	if out, err := ses.Output("pwd"); err != nil || strings.TrimSpace(string(out)) != repo {
		t.Errorf("pwd with no HOME = %q, %v, want %s", out, err, repo)
	}
	t.Setenv("HOME", filepath.Join(repo, "missing"))
	ses2, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses2.Close()
	if err := ses2.Start("true"); err == nil {
		t.Error("a command with no directory to run in started")
	}
}

func TestShellPath(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	if got := shellPath(); got != "/bin/zsh" {
		t.Errorf("$SHELL = %s", got)
	}
	t.Setenv("SHELL", "")
	withBash := t.TempDir()
	if err := os.WriteFile(filepath.Join(withBash, "bash"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", withBash)
	if got := shellPath(); got != "bash" {
		t.Errorf("with bash = %s", got)
	}
	t.Setenv("PATH", t.TempDir())
	if got := shellPath(); got != "/bin/sh" {
		t.Errorf("without bash = %s", got)
	}
}

// Past its connection limit the server hangs up before any handshake.
func TestSSHRefusesConnectionsOverItsLimit(t *testing.T) {
	s, err := newSSHServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.sem = make(chan struct{}) // no room at all
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go s.serve(ln)
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if b, err := io.ReadAll(c); err != nil || len(b) != 0 {
		t.Errorf("got %q, %v, want the connection closed unanswered", b, err)
	}
}

// Something that is not an SSH client gets the banner and, once it is done
// talking, a hang-up: a failed handshake takes nothing else down.
func TestSSHDropsWhatDoesNotSpeakIt(t *testing.T) {
	_, _, _, sock := newTestSSH(t)
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := io.WriteString(c, "GET / HTTP/1.1\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	halfClose(c)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(c)
	if err != nil || !strings.HasPrefix(string(got), "SSH-2.0-apothecary-sidecar") {
		t.Errorf("got %q, %v, want the banner and a hang-up", got, err)
	}
}

func TestStartSSHFailsWithoutASocket(t *testing.T) {
	if _, err := startSSH(filepath.Join(t.TempDir(), "missing", "ssh.sock"), t.TempDir()); err == nil {
		t.Error("a socket in a directory that does not exist was served")
	}
}

// A forward whose request is not the RFC's shape is refused, not guessed at.
func TestSSHDirectTCPIPMalformed(t *testing.T) {
	client, _, _, _ := newTestSSH(t)
	if _, _, err := client.OpenChannel("direct-tcpip", []byte{1, 2}); err == nil {
		t.Error("a malformed forward opened")
	}
}

// A door key file with comments and junk still admits the door, and only it.
func TestDoorKeyFileLines(t *testing.T) {
	_, door, _, _ := newTestSSH(t)
	line := ssh.MarshalAuthorizedKey(door.PublicKey())
	content := append([]byte("# the door\n\nnot a key\n"), line...)
	if err := os.WriteFile(doorKeyPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if !doorKeyAccepts(door.PublicKey()) {
		t.Error("the door key is not found past the comments")
	}
}
