package engine

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"apothecary/internal/account/auth"
	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
	"apothecary/internal/workspace/sshdoor"
	"apothecary/internal/workspace/wsinfo"
)

// fakeSidecarSSH is a workspace's SSH server as a test sees it: it admits
// the key the engine wrote into the fake env — proof the engine wrote it —
// and answers one exec.
type fakeSidecarSSH struct {
	ln   net.Listener
	envp fakeEnvFiles
}

type fakeEnvFiles interface {
	doorKey() []byte
}

func newFakeSidecarSSH(t *testing.T, files fakeEnvFiles) *fakeSidecarSSH {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		pk, _, _, _, err := ssh.ParseAuthorizedKey(files.doorKey())
		if err == nil && bytes.Equal(pk.Marshal(), key.Marshal()) {
			return &ssh.Permissions{}, nil
		}
		return nil, errors.New("not the door key the engine wrote")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, chReqs, err := nc.Accept()
					if err != nil {
						continue
					}
					go func() {
						for r := range chReqs {
							if r.Type == "exec" {
								_ = r.Reply(true, nil)
								_, _ = ch.Write([]byte("hello from the workspace"))
								_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
								_ = ch.Close()
								return
							}
							if r.WantReply {
								_ = r.Reply(false, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	return &fakeSidecarSSH{ln: ln, envp: files}
}

// envDoorKey reads the door key the engine wrote into the fake env.
type envDoorKey struct {
	h   *harness
	env envport.EnvID
}

func (k envDoorKey) doorKey() []byte {
	data, err := k.h.envp.ReadFile(context.Background(), k.env, "/apothecary/ssh_door.pub")
	if err != nil {
		return nil
	}
	return data
}

// sshTargets adapts the workspace service for the door.
type sshTargets struct{ ws *wsinfo.Service }

func (s sshTargets) Lookup(ctx context.Context, workspace string) (sshdoor.Target, error) {
	tg, err := s.ws.SSHTarget(ctx, workspace)
	if err != nil {
		return sshdoor.Target{}, err
	}
	return sshdoor.Target{RunID: tg.RunID, OwnerID: tg.OwnerID, Dial: tg.Dial}, nil
}

// Through the door into a run's workspace: the username is the workspace,
// the key must be the owner's, the door key reaches the workspace first,
// and the session runs in it.
func TestSSHDoorReachesTheRunsWorkspace(t *testing.T) {
	h := newHarness(t, mustFlow(t, reviewFlowDoc))
	ws := h.workspaces()
	users := auth.New(auth.NewMem())
	ctx := context.Background()
	pw := "correct horse battery staple"
	alice, err := users.Setup(ctx, auth.NewUser{Handle: "alice", Email: "a@x", Name: "Alice", Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.CreateUser(ctx, auth.NewUser{Handle: "bob", Email: "b@x", Name: "Bob", Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	register := func(u auth.User) ssh.Signer {
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		signer, _ := ssh.NewSignerFromKey(priv)
		if _, err := users.AddSSHKey(ctx, u.ID, "laptop", string(ssh.MarshalAuthorizedKey(signer.PublicKey()))); err != nil {
			t.Fatal(err)
		}
		return signer
	}
	aliceKey, bobKey := register(alice), register(bob)

	// Alice's run, parked at its gate with a live workspace (the writer
	// agent meets its contract so the gate is reached).
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"writer": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/plan.md", "# Plan\n")
			h.writeFile(t, c.Env, "artifacts/shots/home.png", png)
			return agentport.AgentResult{}, nil
		},
	})
	h.res.UserID = alice.ID
	runID, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "T"}, "rv", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.waitPing(t, event.PingGate)
	v := h.view(t, runID)
	if v.EnvID == "" || v.UserID != alice.ID {
		t.Fatalf("run view = %+v", v)
	}
	env := envport.EnvID(v.EnvID)

	// The workspace's SSH server, reached through the fake provisioner.
	sidecar := newFakeSidecarSSH(t, envDoorKey{h: h, env: env})
	h.envp.SSHDial = func(got envport.EnvID) (net.Conn, error) {
		if got != env {
			return nil, errors.New("wrong env")
		}
		return net.Dial("tcp", sidecar.ln.Addr().String())
	}

	seed := make([]byte, 32)
	_, _ = rand.Read(seed)
	hostKey, _ := sshdoor.KeyFromSeed(seed)
	_, _ = rand.Read(seed)
	doorKey, _ := sshdoor.KeyFromSeed(seed)
	door, err := sshdoor.New(sshdoor.Config{HostKey: hostKey, DoorKey: doorKey, Keys: users, Targets: sshTargets{ws}, Advertise: "door.test:2222"})
	if err != nil {
		t.Fatal(err)
	}
	ws.SetDoorKey(door.DoorPublicKey())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = door.Serve(ln) }()
	defer door.Close()

	connect := func(user string, key ssh.Signer) (*ssh.Client, error) {
		return ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{
			User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)},
			HostKeyCallback: ssh.FixedHostKey(hostKey.PublicKey()), Timeout: 5 * time.Second,
		})
	}
	client, err := connect(v.EnvID, aliceKey)
	if err != nil {
		t.Fatalf("alice into her workspace: %v", err)
	}
	defer client.Close()
	ses, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, err := ses.Output("whoami")
	if err != nil || string(out) != "hello from the workspace" {
		t.Fatalf("exec = %q, %v", out, err)
	}
	ses.Close()
	// The engine wrote the door's key where the sidecar reads it.
	if got, _ := h.envp.ReadFile(ctx, env, "/apothecary/ssh_door.pub"); strings.TrimSpace(string(got)) != strings.TrimSpace(door.DoorPublicKey()) {
		t.Errorf("door key in the workspace = %q", got)
	}
	// Bob's key opens nothing of Alice's; an unknown workspace opens nothing.
	if c, err := connect(v.EnvID, bobKey); err == nil {
		c.Close()
		t.Error("bob got into alice's workspace")
	}
	if c, err := connect("env-999", aliceKey); err == nil {
		c.Close()
		t.Error("an unknown workspace opened")
	}
	// What the run page is told.
	info, err := ws.SSHInfo(ctx, runID)
	if err != nil || info.Workspace != v.EnvID {
		t.Errorf("SSHInfo = %+v, %v", info, err)
	}
	// An archived run's workspace is no target, nor is an unknown name.
	if _, err := ws.SSHTarget(ctx, "env-999"); !errors.Is(err, wsinfo.ErrNoWorkspace) {
		t.Errorf("unknown workspace: %v", err)
	}
}
