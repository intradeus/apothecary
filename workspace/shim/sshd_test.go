package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
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
	sock = filepath.Join(os.TempDir(), "apo-ssh-"+strings.TrimPrefix(filepath.Base(dir), "Test")+".sock")
	t.Cleanup(func() { os.Remove(sock) })
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
