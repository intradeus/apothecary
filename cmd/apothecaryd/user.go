package main

// Accounts from the terminal (TODO 25): what an operator with shell access
// can do when the UI cannot — list who exists, reset a password (the way
// back in after a lockout), make or unmake an admin. Everything else about
// accounts is the UI's.

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"apothecary/internal/account/auth"
	"apothecary/internal/pgstore"
	"apothecary/internal/platform/config"
)

func cmdUser(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: apothecaryd user list | reset-password <handle> [-password <pw>] | set-admin <handle> [-off]")
	}
	boot, err := config.FromEnv()
	if err != nil {
		return err
	}
	ctx := context.Background()
	store, err := pgstore.Open(ctx, boot.PostgresDSN)
	if err != nil {
		return err
	}
	defer store.Close()
	svc := auth.New(store)

	switch args[0] {
	case "list":
		users, err := svc.ListUsers(ctx)
		if err != nil {
			return err
		}
		if len(users) == 0 {
			fmt.Println("no accounts yet — open the UI to set up the first one")
			return nil
		}
		for _, u := range users {
			flags := ""
			if u.Admin {
				flags += " admin"
			}
			if u.Disabled() {
				flags += " disabled"
			}
			fmt.Printf("%s\t%s\t%s\t%s%s\n", u.ID, u.Handle, u.Email, u.Name, flags)
		}
		return nil
	case "reset-password":
		fs := flag.NewFlagSet("user reset-password", flag.ExitOnError)
		pw := fs.String("password", "", "the new password (prompted when omitted)")
		_ = fs.Parse(args[1:])
		if fs.NArg() != 1 {
			return errors.New("usage: apothecaryd user reset-password <handle|email> [-password <pw>]")
		}
		u, err := svc.GetUserByLogin(ctx, fs.Arg(0))
		if err != nil {
			return err
		}
		password := *pw
		if password == "" {
			if password, err = promptPassword(); err != nil {
				return err
			}
		}
		if err := svc.SetPassword(ctx, u.ID, password); err != nil {
			return err
		}
		fmt.Printf("password set for %s; every session signed out\n", u.Handle)
		return nil
	case "set-admin":
		fs := flag.NewFlagSet("user set-admin", flag.ExitOnError)
		off := fs.Bool("off", false, "remove instance administration instead")
		_ = fs.Parse(args[1:])
		if fs.NArg() != 1 {
			return errors.New("usage: apothecaryd user set-admin <handle|email> [-off]")
		}
		u, err := svc.GetUserByLogin(ctx, fs.Arg(0))
		if err != nil {
			return err
		}
		if _, err := svc.SetAdmin(ctx, u.ID, !*off); err != nil {
			return err
		}
		if *off {
			fmt.Printf("%s is no longer an admin\n", u.Handle)
		} else {
			fmt.Printf("%s is an admin\n", u.Handle)
		}
		return nil
	default:
		return fmt.Errorf("unknown user command %q", args[0])
	}
}

// promptPassword reads one line from stdin: a pipe (`echo pw | …`) or a
// typed line. No raw-terminal dependency, so the input echoes; the
// -password flag exists for scripts that mind.
func promptPassword() (string, error) {
	fmt.Fprint(os.Stderr, "new password: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("no password given")
	}
	return line, nil
}
