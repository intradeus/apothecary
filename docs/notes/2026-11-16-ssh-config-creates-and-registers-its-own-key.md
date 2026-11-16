# ssh-config creates and registers its own key

Extends `2026-11-11-kubernetes-only-one-chart-tunnels-and-the-apothecary-client.md`,
whose `ssh-config` wrote only the `Host *.apothecary` block and left the key
to the person: register one under Account → SSH keys, and hope ssh offered
it.

Why: the block named no key, so ssh offered whatever the agent and the
default files held, and a person with no registered key was refused at the
door with nothing to say why. The ticket asked for the command to make a
key (`ssh-keygen -t rsa -b 4096`), register it and name it in the block,
on macOS, Linux and Windows alike. The task's owner chose, over the
ticket's letter, an Ed25519 key generated in the client (`crypto/ed25519`
and `golang.org/x/crypto/ssh`) without a passphrase: no external binary to
find on three platforms, no prompt, and a key the engine accepts (it
refuses only RSA under 2048 bits).

What now: `apothecary ssh-config` (`internal/client/cli/ssh.go`,
`sshkey.go`):

- needs a sign-in, then asks the engine for the account's keys
  (`GET /api/me/ssh-keys`) before it writes anything: a token the engine no
  longer takes ends it with "sign in again" and nothing on disk;
- makes `~/.ssh/apothecary_ed25519` (OpenSSH format, comment
  `apothecary@<hostname>`) unless it is there, with `O_EXCL` — never
  overwriting, refusing a planted symlink — and owner-only before a byte is
  written; `.pub` beside it;
- reuses a key that is there, never rewriting it: one with a passphrase is
  read through the public key its file carries, one readable by others is
  tightened (and the person told), one it cannot read stops it with the
  file named, to be moved aside by hand;
- registers the key (`POST /api/me/ssh-keys`) unless its SHA256 fingerprint
  is listed already, named `<hostname> (apothecary ssh-config)` cut to the
  engine's 64 characters. A 409 is the key on another account: it stops,
  writes no config and says how out (remove it there, or move the key
  aside); it never deletes or replaces a key on its own;
- writes the block with `IdentityFile` (forward slashes, `%` doubled, quoted
  on a space) and `IdentitiesOnly yes`.

No passphrase: the key is protected by its permissions instead. On Unix it
is 0600 (`sshkey_unix.go`). On Windows it gets a protected DACL — nothing
inherited — granting the current user full control and nobody else
anything, which is what Windows' OpenSSH asks of a private key
(`sshkey_windows.go`). That DACL is set by name (`SetNamedSecurityInfo`)
rather than on the handle the key is written through: `os.OpenFile`'s
handle carries no `WRITE_DAC`. It is still set before the key's bytes are
written, and a failure removes the file. Anyone who can read the person's
files can use the key until it is revoked under Account → SSH keys.

`IdentitiesOnly yes`: only the registered key is offered, so the agent's
other keys do not count as failed attempts against the door's per-user
throttle or ssh's `MaxAuthTries`. A person who registered a key of their
own before now reaches `*.apothecary` with this one; the docs say so.

One key per machine: signing in to a second engine and running
`ssh-config` again registers the same public key there (fingerprints are
unique per engine). A key made but not registered (a network error) is
registered on the next run; one registered whose config failed is found
listed and the config written.

Held by `TestSSHConfig` (key made 0600 and Ed25519, `.pub` matching, one
registration, block lines, a rerun reusing the key and registering
nothing), `TestSSHConfigWhenTheEngineRefusesTheToken`,
`TestSSHConfigWhenTheKeyIsAnotherAccounts`, `TestSSHConfigReusesTheKeyThere`,
`TestSSHConfigKeyTrouble`, `TestSSHConfigQuotesTheKeysPath` and `TestKeyName`
in `internal/client/cli`, and `TestSSHKeys` in `internal/client/api` (the
wire, against the engine's `auth.SSHKey` and `httpapi.SSHKeyRequest`). The
Windows DACL is compiled by `task go:lint` (`GOOS=windows go vet`), not
run; a manual Windows smoke with `icacls` holds it before a release.
