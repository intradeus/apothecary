# Kata runs in CI, with a nested cluster inside its sandbox

## Why

The Kata path had the most code that no CI job ran: the `/dev` prelude in
`apo-dockerd`, the out-of-cluster dial by exec, a VM's own emptyDirs, the
userspace tunnel, VM sizing. `task sandbox:test:kata` existed, but only by
hand on a host with KVM. And the runtime table claims Kata gives nested
Kubernetes and user namespaces (`envport.RuntimeKata`), the reason to
choose it over sysbox, while nothing checked that claim on the stock guest
kernel. The spike ran `task sandbox:test:k8s` inside a Kata sandbox, but on a
guest kernel rebuilt with WireGuard (the plan's appendix A). Installing Kata
on a real box was a page of manual steps.

## What now

- **One pin, one config script.** `deploy/kata/kata.env` holds the Kata
  release, its sha256 and the k3s image. `deploy/kata/configure.sh` writes
  `configuration-apothecary.toml` from the release's Cloud Hypervisor
  configuration and checks every edit took. `rig.sh` and `install.sh` source
  the first and run the second. The Taskfile reads the pins through
  `dotenv`. `TestToolPinsMatchCI` refuses a copy of any pin elsewhere.
  `rig.sh` now rewrites the configuration on every `up`, so an edit reaches
  a volume unpacked before it.
- **`deploy/k3s/install.sh --kata`.** It refuses anything but amd64 with
  `/dev/kvm`. It unpacks the pinned release into `/opt/kata` (a version
  marker skips the download on a rerun) and configures it. It adds a `kata`
  runtime to k3s's containerd template beside `sysbox-runc`, and the `kata`
  RuntimeClass with the overhead the rig measured. A second smoke pod runs
  a privileged dind in a sized Kata VM with its docker data on an emptyDir.
  The chart is installed with `workspaces.runtimes={sysbox,kata}`, so sysbox
  stays the default.
- **Nested Kubernetes inside the sandbox** (`liveNestedKubernetes`, the end
  of `TestLiveK8sLifecycle`).
  - With `APOTHECARY_LIVE_NESTED_K8S=<k3s image>`, the test starts a k3s
    node on the sandbox's docker daemon. Its images come through the run's
    gateway. On that node it runs a `hostUsers: false` pod (the assistant
    pod's shape) and checks its uid map: id 0 at an outside id the nested
    kubelet picked, never 0. That is what `task sandbox:test:k8s` needs of a
    sandbox beyond what the lifecycle test already covers (docker, the
    tunnel, exec, files).
  - Running the full suite inside the sandbox would build this tree's
    images again in the VM: about 25 GB and ten minutes, on a runner that
    has neither to spare. So this check stands in for it.
  - `sandbox:test:kata` always sets the variable. A kata run without it
    skips at the end through livetest, so under `APOTHECARY_LIVE_STRICT` it
    fails. It runs only for runtimes that claim both NestedKubernetes and
    UserNamespaces. Under sysbox the pod cannot start, by design.
- **CI: a third `sandbox-test` suite, `kata`, strict.**
  - It loads the four sandbox images from the cache, frees disk as the k8s
    suite does, installs the pinned kubectl and loads the wireguard module.
    The gateways run on the rig's node, so on the runner's kernel.
  - GitHub's Linux runners are VMs that expose `/dev/kvm`, so the Kata VMs
    run nested. Without it, the job fails with an error naming it; it never
    skips.
  - The test sizes its sandbox at 4 CPUs and 8 GiB, plus the RuntimeClass
    overhead. That fits a 16 GB runner, not a 7 GB one.
- **EKS** (`docs/kubernetes.md`): Kata needs KVM on the sandbox nodes:
  `*.metal`, or C8i, M8i or R8i with nested virtualization on. amd64 only.
  Not tried.

## Tests that hold it

- `TestToolPinsMatchCI` (`internal/devtools/devcontainer`):
  - every kubectl and k3d copy in `ci.yml` is the devcontainer's;
  - the Kata pins live in `kata.env` alone;
  - `sandbox:test:kata` reads them.
- `liveNestedKubernetes`, run by `task sandbox:test:kata` and CI's `kata`
  suite. The script passed under the privileged runtime's shape (docker:dind,
  privileged, on a laptop): the node was Ready in 6 s and the pod mapped 0 at
  2155610112.
- `install.sh` and the rig: shellcheck (`task repo:lint`). The smoke pods
  were rendered and read. Running the installer needs a fresh KVM box.

Not run here: the Kata suite itself. The first CI run is its check, the
runner's `/dev/kvm` included.
