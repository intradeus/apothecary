# DESIGN-FILES authored — the artifact system redesign (design only)

`docs/DESIGN-FILES.md` is the target architecture for the file subsystem:
"artifact" retreats to the flow language; storage splits into **attachments**
(human input; ownership structural — ticket/run/answer columns with CHECKs)
and **snapshots** (engine-captured record; CHECK'd kind output|log|ping_file)
over a **content-addressed blob store** (sha256 identity, dedup, verify-on-
read, mark-and-sweep GC, no refcounts). Events become the sole authority for
run-scoped facts (`snapshot_captured` gains the ping id today's event lacks;
the snapshots table becomes a rebuildable projection). Ten verified
conflations dissolve by construction — including the ticket-attachment blob
leak, snapshot re-delivery into workspaces, finished runs listing zero ping
files, and inline-served workspace-authored HTML (trust-class headers).
**Nothing is implemented yet** — four shippable phases are specified in §15;
current code is unchanged.
