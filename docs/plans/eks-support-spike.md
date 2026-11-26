use shared, medium-size nodes for your own projects, not one instance per run and not one big VM-partitioned node. Karpenter works for adding nodes, but today it must never move or remove a sandbox node on its own, because Apothecary doesn't survive losing a sandbox pod gracefully.

Pricing (us-east-1, on-demand)

EC2 charges the same per vCPU at every size in a family, so the instance size alone doesn't change the bill.

┌──────────────┬──────────────┬─────────┬───────────────────────────────────────────────────────┬────────────────┐
│   Instance   │  vCPU / RAM  │   $/h   │              Fits (4 vCPU / 12 GiB runs)              │   $/run-hour   │
├──────────────┼──────────────┼─────────┼───────────────────────────────────────────────────────┼────────────────┤
│ m8i.xlarge   │ 4 / 16 GiB   │ ~$0.212 │ 1                                                     │ $0.21          │
├──────────────┼──────────────┼─────────┼───────────────────────────────────────────────────────┼────────────────┤
│ m8i.4xlarge  │ 16 / 64 GiB  │ ~$0.847 │ 4 at full reservation; ~6–8 if limits exceed requests │ $0.21 → ~$0.11 │
├──────────────┼──────────────┼─────────┼───────────────────────────────────────────────────────┼────────────────┤
│ m8i.16xlarge │ 64 / 256 GiB │ ~$3.39  │ ~18 at full reservation                               │ $0.19          │
└──────────────┴──────────────┴─────────┴───────────────────────────────────────────────────────┴────────────────┘

- Spot is about half price (m8i.4xlarge ~$0.41/h). But a Spot reclaim is a lost node, which is the failure case described below.
- Fixed costs per month:
  - EKS control plane: $0.10/h, ~$73/mo.
  - A small node for the engine and Postgres, or RDS.
  - A NAT gateway: ~$32/mo plus data.
  - EBS gp3 for each kept run's claim: ~$0.08/GB-mo, so a few dollars per run.

Example: 5 parallel runs at 4 vCPU / 12 GiB each.

┌────────────────────────────────────────────────────┬───────────────────┐
│                       Usage                        │ Compute per month │
├────────────────────────────────────────────────────┼───────────────────┤
│ 8 h a day, 22 days, nodes scaled to zero otherwise │ ≈ $190            │
├────────────────────────────────────────────────────┼───────────────────┤
│ 24/7                                               │ ≈ $775            │
└────────────────────────────────────────────────────┴───────────────────┘

Then add ~$150–250/mo of fixed costs. Stop-on-idle plus Karpenter scaling nodes down is what keeps you near the first row.

Price sources: m8i.xlarge (https://www.devzero.io/instances/aws/m8i.xlarge), m8i.4xlarge (https://instances.vantage.sh/aws/ec2/m8i.4xlarge), m8i.16xlarge (https://www.devzero.io/instances/aws/m8i.16xlarge), EKS pricing (https://aws.amazon.com/eks/pricing/). These are third-party mirrors, so check the AWS pricing page before budgeting.

The three layouts

1. One large node, partitioned with VMs (Kata):
- It gets you no pricing advantage.
- One node is a single point of failure for every run.
- It never scales to zero.
- Kata VMs hold their memory, so you can't overcommit.
- On EKS it needs a metal instance or nested virtualization, which managed node groups don't expose. Karpenter has a setting for it, and docs/kubernetes.md says this is untested.
- Don't do this.

2. One run per small instance (the GitHub/GitLab runner model). The EC2 instance itself (AWS's Nitro hypervisor) becomes the boundary.
- For: strong isolation without Kata. Running plain, or even privileged, inside is safe enough, because an escape only reaches that run's node. You do need IMDSv2 with hop limit 1 and a minimal node IAM role.
- Against:
  - Each new run waits for a node to boot and pull images, likely a minute or more cold. The tools bundle alone is ~2.3 GB, so you'd want a pre-baked AMI or warm nodes.
  - No overcommit, so you pay for 12 GiB even while the agent is waiting on the LLM.
  - Per-node overhead: DaemonSets, ENIs, and system reserve eating into a 16 GiB node.
- When: untrusted code or other tenants. Not your own projects.

3. Shared medium nodes (m8i.4xlarge or .8xlarge) with a shared-kernel runtime: recommended.
- Runs pack 4–8 per node. Overcommit (memory requests below limits) is where you save the most money, because most runs sit idle much of the time.
- New runs start in seconds on a warm node, with images already cached.
- Runtimes:
  - Today: sysbox.
  - Later: plain for docker-free projects, and userns (once built) for docker and nested Kubernetes.
  - Kata: only if something still needs it after userns exists. If so, put it in a separate Karpenter NodePool with nested virtualization on.
- Is plain safe enough on a shared node? For your own code, yes. It keeps the default seccomp and AppArmor profiles and its own user namespace, which makes it the most restricted shared-kernel option. The gap is the same one any shared-kernel runtime has: a kernel exploit reaches the node, its other sandboxes, and unfiltered network access.

Karpenter: is Apothecary resilient to node changes? Not yet

Adding nodes is fine. Removing or replacing them is not:
- Losing a pod means starting over. When a sandbox pod disappears without the engine putting it to sleep, the status reads "absent". The engine then provisions a fresh sandbox and logs "whatever was changed by hand in the old container is gone with it" (internal/engine/provision.go). That happens on a consolidation, a node expiring, a drift replacement, or a Spot reclaim.
  - The run's branch survives.
  - The in-sandbox state is gone: containers, installed tools, anything uncommitted.
  - A stage running at that moment is interrupted.
  - I didn't check whether the fresh provision reuses the run's existing claim.
- Only a planned sleep is graceful. It writes an .asleep marker and deletes the pod. A woken pod reuses the claim.
- Nothing protects sandbox pods from eviction. They carry no karpenter.sh/do-not-disrupt annotation and no PodDisruptionBudget.
- Storage is tied to one zone. EBS volumes are single-AZ, so a woken sandbox has to land in its volume's zone. Karpenter follows volume affinity, but keep the NodePool's zones and the WaitForFirstConsumer storage class in line.

To use Karpenter safely now:
- Give sandboxes a dedicated, tainted NodePool.
- Set consolidationPolicy: WhenEmpty, so a node is removed only once its runs have slept or finished. Don't use WhenEmptyOrUnderutilized.
- Set a long expireAfter, or a disruption budget that only allows changes off-hours.
- No Spot for sandboxes yet.

Engine changes that would make node changes routine (good ticket material):
1. Add karpenter.sh/do-not-disrupt: "true" to every sandbox pod while it's awake. That's small.
2. Treat an unplanned pod loss whose claim still exists as asleep, not absent. The engine would then wake it on the same volume instead of re-provisioning. That turns a lost node into a short pause, and makes Spot viable.
3. Make sure per-project gateway restarts don't break the tunnels of running sandboxes (I haven't checked this).

What I'd do: shared m8i.4xlarge sandbox nodes in a Karpenter pool that only removes empty nodes, sysbox now, plain and userns as they land, and changes 1–2 above before turning on consolidation or Spot. Keep one-run-per-instance in reserve for when you run code you don't trust.

Want me to add changes 1–2 as a third ticket next to the two that are waiting on the token?