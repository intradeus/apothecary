package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"apothecary/internal/client/api"
)

// pollUnit is what the engine's polling interval counts in. A var for tests.
var pollUnit = time.Second

const loginUsage = "usage: apothecary login <url> [--token <personal token>] [--no-browser]"

// cmdLogin signs this machine in. By default through the browser: the
// engine opens a sign-in, the person approves it on the page (where they
// are usually signed in already), and this machine gets a token of its own
// that expires after a stretch without use — every use slides it, like a
// browser session. --token takes a personal token made in the UI instead
// (Account → API tokens), for scripts and CI.
func cmdLogin(ctx context.Context, args []string, env Env, p *printer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	token := fs.String("token", "", "a personal token to sign in with")
	noBrowser := fs.Bool("no-browser", false, "print the address instead of opening it")
	url, rest := splitURL(args)
	if err := fs.Parse(rest); err != nil {
		return errUsage(loginUsage)
	}
	if url == "" && fs.NArg() > 0 {
		url = fs.Arg(0)
	}
	if url == "" {
		return errUsage(loginUsage)
	}
	c, err := api.New(url, "")
	if err != nil {
		return err
	}
	cfg := Config{URL: c.Base()}
	var handle string
	if *token != "" {
		authed, err := api.New(cfg.URL, *token)
		if err != nil {
			return err
		}
		me, err := authed.Me(ctx)
		if err != nil {
			return err
		}
		cfg.Token, handle = *token, me.Handle
	} else {
		tok, err := approveInBrowser(ctx, c, env, p, !*noBrowser)
		if err != nil {
			return err
		}
		cfg.Token, cfg.TokenID, handle = tok.Secret, tok.ID, tok.Handle
	}
	if err := saveConfig(env, cfg); err != nil {
		return err
	}
	p.say("Signed in to %s as %s.", cfg.URL, handle)
	forgetVersionCheck(env)
	warnDrift(ctx, env, p)
	return nil
}

// approveInBrowser opens a sign-in on the engine and waits for its person
// to approve it.
func approveInBrowser(ctx context.Context, c *api.Client, env Env, p *printer, open bool) (api.Token, error) {
	l, err := c.StartLogin(ctx, tokenName(env))
	if err != nil {
		return api.Token{}, err
	}
	p.say("To sign in, approve code %s on this page:\n  %s", l.UserCode, l.VerificationURL)
	if open && env.OpenBrowser != nil {
		_ = env.OpenBrowser(l.VerificationURL)
	}
	p.say("Waiting for approval…")
	interval := time.Duration(max(l.Interval, 1)) * pollUnit
	deadline := time.Now().Add(time.Duration(max(l.ExpiresIn, 1)) * pollUnit)
	for {
		tok, err := c.PollLogin(ctx, l.DeviceCode)
		if !errors.Is(err, api.ErrLoginPending) {
			return tok, err
		}
		if time.Now().After(deadline) {
			return api.Token{}, errors.New("the sign-in was not approved in time; run apothecary login again")
		}
		select {
		case <-ctx.Done():
			return api.Token{}, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// splitURL takes the engine address off wherever it sits among the flags
// (only --token takes a value).
func splitURL(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--token" || a == "-token" {
			i++
			continue
		}
		if len(a) > 0 && a[0] == '-' {
			continue
		}
		return a, append(append([]string{}, args[:i]...), args[i+1:]...)
	}
	return "", args
}

func tokenName(env Env) string {
	if env.Hostname == "" {
		return "apothecary CLI"
	}
	return "apothecary CLI on " + env.Hostname
}

// cmdLogout revokes this machine's sign-in and forgets it. A pasted
// personal token stays valid: it was made in the UI, and is revoked there.
func cmdLogout(ctx context.Context, env Env, p *printer) error {
	c, cfg, err := signedIn(env)
	if err != nil {
		return err
	}
	if cfg.TokenID != "" {
		if err := c.RevokeToken(ctx, cfg.TokenID); err != nil && !api.IsUnauthorized(err) {
			return fmt.Errorf("revoke this machine's sign-in: %w", err)
		}
	}
	if err := removeConfig(env); err != nil {
		return err
	}
	if cfg.TokenID == "" {
		p.say("Signed out. The token you pasted still works until you revoke it (Account → API tokens).")
		return nil
	}
	p.say("Signed out of %s, and this machine's sign-in is revoked.", cfg.URL)
	return nil
}
