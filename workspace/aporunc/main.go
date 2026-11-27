// Command apo-runc is the runc a sandbox's docker daemon runs its containers
// with under the userns runtime (deploy/images/sandbox/apo-dockerd,
// APO_USERNS_NESTING): runc itself, after two changes to a container's spec.
//
// The sandbox is a pod in a user namespace of its own, whose /sys is a
// read-only sysfs. The kernel refuses a fresh sysfs in a user namespace
// unless one at least as permissive is already visible (mnt_already_visible),
// so a privileged container, whose spec asks for a read-write /sys, fails at
// create. Nothing in the sandbox can make the pod's /sys writable: a sysfs
// superblock belongs to the initial user namespace. On create and run, this
// wrapper adds "ro" to a writable sysfs mounted at /sys, which the kernel then
// allows: the container keeps a sysfs of its own network namespace, read-only.
//
// The second change: a container with no CPU quota gets the sandbox's own
// (its cgroup's cpu.max). The sandbox's limit sits outside the container's
// cgroup namespace, so without it Go's GOMAXPROCS and Node's available
// parallelism count every CPU of the node.
//
// Every other mount and field keeps its value, and every other subcommand
// reaches runc untouched.
//
// Self-contained (pure stdlib), like workspace/shim and workspace/gateway:
// the sandbox image builds it standalone (deploy/images/sandbox/Dockerfile).
package main

import (
	"os"
	"syscall"
)

func main() { os.Exit(run(os.Args, os.Getenv, syscall.Exec, os.Stderr)) }
