package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"apothecary/internal/platform/wstunnel"
)

const (
	sshBlockStart = "# >>> apothecary >>>"
	sshBlockEnd   = "# <<< apothecary <<<"
)

// cmdSSHConfig writes the block of ~/.ssh/config that sends every host named
// <workspace>.apothecary through this client to the engine's SSH door: once,
// and then ssh and VS Code Remote-SSH reach any workspace by name. The door
// has one host key for every workspace, pinned once under one alias. Run
// again, it replaces its own block and nothing else.
func cmdSSHConfig(env Env, p *printer) error {
	_, cfg, err := signedIn(env)
	if err != nil {
		return err
	}
	if env.Home == "" || env.Executable == "" {
		return errors.New("cannot tell where ~/.ssh or this program is")
	}
	u, _ := url.Parse(cfg.URL)
	dir := filepath.Join(env.Home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "config")
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	block := strings.Join([]string{
		sshBlockStart,
		"# Written by `apothecary ssh-config` for " + cfg.URL + "; run it again to update.",
		"Host *" + wstunnel.SSHHostSuffix,
		"    ProxyCommand " + quoteArg(env.Executable) + " ssh-proxy %h",
		"    User apothecary",
		"    HostKeyAlias apothecary-" + u.Hostname(),
		"    StrictHostKeyChecking accept-new",
		sshBlockEnd,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(withBlock(string(old), block)), 0o600); err != nil {
		return err
	}
	p.say("Wrote %s: ssh <workspace>%s now reaches a run's workspace through %s.", path, wstunnel.SSHHostSuffix, cfg.URL)
	return nil
}

// withBlock is the config with our block replaced, or appended at the end
// (a Host block placed above global options would capture them).
func withBlock(config, block string) string {
	start := strings.Index(config, sshBlockStart)
	end := strings.Index(config, sshBlockEnd)
	if start >= 0 && end > start {
		rest := config[end+len(sshBlockEnd):]
		rest = strings.TrimPrefix(rest, "\n")
		return config[:start] + block + rest
	}
	if config != "" && !strings.HasSuffix(config, "\n") {
		config += "\n"
	}
	if config != "" {
		config += "\n"
	}
	return config + block
}

// quoteArg quotes a path for ssh_config when it holds a space.
func quoteArg(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}

// cmdSSHProxy is the ProxyCommand: one SSH connection to the door for the
// workspace the host names, over stdin and stdout.
func cmdSSHProxy(ctx context.Context, args []string, env Env) error {
	if len(args) != 1 {
		return errUsage("usage: apothecary ssh-proxy <workspace>" + wstunnel.SSHHostSuffix)
	}
	workspace := strings.TrimSuffix(args[0], wstunnel.SSHHostSuffix)
	if workspace == "" {
		return errUsage(fmt.Sprintf("%q names no workspace", args[0]))
	}
	c, _, err := signedIn(env)
	if err != nil {
		return err
	}
	conn, err := c.DialSSH(ctx, workspace)
	if err != nil {
		return err
	}
	// stdin ending says so to the door (a half close) and the output is
	// read to its end; the door ending ends it all. The input may sit in a
	// read of stdin when the door ends first, and is left there.
	inDone, outDone := make(chan struct{}), make(chan struct{})
	go func() { _, _ = io.Copy(conn, env.Stdin); close(inDone) }()
	go func() { _, _ = io.Copy(env.Stdout, conn); close(outDone) }()
	select {
	case <-inDone:
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		select {
		case <-outDone:
		case <-ctx.Done():
		}
	case <-outDone:
	case <-ctx.Done():
	}
	conn.Close()
	<-outDone
	return nil
}
