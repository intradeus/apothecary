package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"apothecary/internal/client/api"
	"apothecary/internal/platform/wstunnel"
)

const (
	sshBlockStart = "# >>> apothecary >>>"
	sshBlockEnd   = "# <<< apothecary <<<"
)

// cmdSSHConfig sets this machine up for the engine's SSH door, once: its
// own key (~/.ssh/apothecary_ed25519, made unless it is there), registered
// on the signed-in person's account, and the block of ~/.ssh/config that
// sends every host named <workspace>.apothecary through this client to the
// door with that key; then ssh and VS Code Remote-SSH reach any workspace
// by name. The door has one host key for every workspace, pinned once
// under one alias. Run again, it reuses the key and replaces its own block
// and nothing else. Nothing is written before the engine takes the
// sign-in.
func cmdSSHConfig(ctx context.Context, env Env, p *printer) error {
	c, cfg, err := signedIn(env)
	if err != nil {
		return err
	}
	if env.Home == "" || env.Executable == "" {
		return errors.New("cannot tell where ~/.ssh or this program is")
	}
	registered, err := c.SSHKeys(ctx)
	if err != nil {
		return err
	}
	u, _ := url.Parse(cfg.URL)
	dir := filepath.Join(env.Home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	key, err := loadOrCreateKey(dir, keyComment(env.Hostname))
	if err != nil {
		return err
	}
	fp := ssh.FingerprintSHA256(key.pub)
	how := "reused"
	if key.created {
		how = "created"
	}
	p.say("Key %s (%s): %s.", key.path, fp, how)
	if key.tightened {
		p.say("Others could read it: it is now readable by you only.")
	}
	if err := register(ctx, c, registered, key, fp, cfg.URL, keyName(env.Hostname), p); err != nil {
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
		"    IdentityFile " + identityFileArg(key.path),
		"    IdentitiesOnly yes",
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

// register puts the key on the person's account unless it is among the
// keys registered there already. A key on another account is not taken
// over, removed or replaced: the person says which.
func register(ctx context.Context, c *api.Client, registered []api.SSHKey, key machineKey, fp, engine, name string, p *printer) error {
	if k, ok := findKey(registered, fp); ok {
		p.say("Already registered on your account as %q.", k.Name)
		return nil
	}
	k, err := c.AddSSHKey(ctx, name, authorizedLine(key.pub, key.comment))
	var refused *api.Error
	if errors.As(err, &refused) && refused.Status == http.StatusConflict {
		// The engine refuses any fingerprint it holds, this account's too:
		// another ssh-config, or the UI, may have registered the key since
		// the list.
		again, lerr := c.SSHKeys(ctx)
		if lerr != nil {
			return lerr
		}
		if k, ok := findKey(again, fp); ok {
			p.say("Already registered on your account as %q.", k.Name)
			return nil
		}
		return fmt.Errorf("%s is registered on another account of %s (%v): remove it there (Account → SSH keys), or move %s* aside and run apothecary ssh-config again", key.path, engine, err, key.path)
	}
	if err != nil {
		return err
	}
	p.say("Registered it on your account as %q.", k.Name)
	return nil
}

// findKey is the key of keys with fingerprint fp.
func findKey(keys []api.SSHKey, fp string) (api.SSHKey, bool) {
	for _, k := range keys {
		if k.Fingerprint == fp {
			return k, true
		}
	}
	return api.SSHKey{}, false
}

// keyComment is the key's comment: who made it, on which machine.
func keyComment(hostname string) string {
	if hostname == "" {
		return "apothecary"
	}
	return "apothecary@" + hostname
}

// keyName is the key's name on the account: the machine's, within the
// engine's 64 characters.
func keyName(hostname string) string {
	if hostname == "" {
		return "apothecary ssh-config"
	}
	const suffix = " (apothecary ssh-config)"
	name := []rune(hostname + suffix)
	if len(name) > 64 {
		name = name[:64]
	}
	return strings.TrimSpace(string(name))
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
