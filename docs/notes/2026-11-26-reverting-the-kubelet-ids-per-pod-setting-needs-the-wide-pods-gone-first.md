# Reverting the kubelet idsPerPod setting needs the wide pods gone first

Corrects 2026-11-26-sysbox-cannot-run-nested-user-namespaced-pods-even-with-a-wider-id-range.
That note says to revert the spike's kubelet setting by deleting the drop-in
and the `kubelet:` lines in `/etc/subuid` and `/etc/subgid`, then restarting
k3s, and calls the setting harmless if kept. The revert done that way took
the host's cluster down.

## Why

On the k3s host, the revert was done while one sandbox pod still ran with
the 4194304-id mapping it got under the setting. k3s then crash-looped:

```
failed to create kubelet: create user namespace manager: record pod mappings
for existing pod "<uid>": wrong user namespace length 4194304
```

At startup the kubelet records the mapping of every running user-namespaced
pod. It refuses one whose length is not its current `idsPerPod`, and exits.
The API was down for about seven minutes. Containers kept running: k3s's
containerd outlives a k3s restart. The engine's kubectl calls failed until
the API came back.

Turning the setting on did not hit this: the kubelet started with 4194304
while pods mapped at 65536 ran. The check that bit goes one way only, so
widening the range is safe and narrowing it is not.

## What now

To revert a wider `idsPerPod`, in this order:

1. Find every pod whose length is the wide one: each `hostUsers: false` pod's
   `/proc/self/uid_map`, third field. On Apothecary's host these are the
   sandbox pods (`apothecary-workspaces`).
2. Get rid of them: stop or sleep their runs from Apothecary, or delete the
   pod of an abandoned run. Its claim, the checkout, stays.
3. Only then delete the drop-in and the `/etc/subuid` and `/etc/subgid`
   lines, and restart k3s.

If k3s is already crash-looping on this error, put the setting back and
restart k3s. It comes up with every pod as it was. Then follow the steps
above.

Kept, the setting is still harmless while it stays. The danger is only in
removing or lowering it with wide pods running. Which way the check goes
was seen on k3s v1.36.4 only.

## Tests that hold it

None: an operations procedure for the host, measured by hand.
