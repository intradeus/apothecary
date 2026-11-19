// Package assistant runs each person's assistant pod on Kubernetes
// (docs/assistant-plan.md, phase 2): one light pod per person, a volume of
// theirs that outlives it, a gateway of theirs, and a manager that starts,
// reuses and stops it. The conversation reaches it through
// assistantport.Pods.
//
// Two init containers seed what the image leaves out: shim-seed copies the
// shim (the shim image) into the pod, tools-seed the harnesses the assistant
// runs (the tools image's musl builds of claude-code and opencode) onto the
// person's volume, once per bundle version. Then four containers:
//
//   - tunnel (a native sidecar, the gateway image): brings up the WireGuard
//     link to the person's gateway, the pod's only way out. The one holder of
//     a capability, NET_ADMIN.
//   - relay (a native sidecar, the shim image): the engine's way in, to the
//     agent host's socket, behind the pod's relay token.
//   - agent (the assistant image): the shim's agent host. Each session runs
//     in it as one Landlock-confined process (shim confine). It reads the
//     repositories and writes /state; it never holds a repository credential.
//   - git (the assistant image): writes the repositories; a fetch runs in it
//     with the credential on its stdin for the length of the fetch.
//
// Every container runs without privilege: a user namespace of the pod's own
// (hostUsers: false), uid 1000 (root inside the namespace only for the
// tunnel), every capability dropped but the tunnel's one, no privilege
// escalation, the runtime's seccomp profile, a read-only root, no service
// account token.
package assistant

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
	"time"

	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/adapters/kube"
	"apothecary/internal/ports/assistantport"
	"apothecary/internal/ports/envport"
)

// The pod's containers.
const (
	tunnelContainer = "tunnel"
	relayContainer  = "relay"
	agentContainer  = "agent"
	gitContainer    = "git"
)

// Paths inside the pod.
const (
	reposRoot = "/workshops"
	stateRoot = "/state"
	sockDir   = "/run/apo"
	netDir    = "/run/apo-net"
	shimDir   = "/opt/apothecary/bin"
	shimBin   = shimDir + "/apothecary-shim"
	toolsDir  = "/opt/apothecary/tools"
	gitBin    = "/usr/bin/git"
	rgBin     = "/usr/bin/rg"
	// loader is the image's link to its musl loader: execve opens it for
	// execution, so a session's rules list it (the image makes the link).
	loader    = "/opt/apothecary/ld"
	toolsMusl = toolsDir + "/musl"
	agentSock = "sidecar.sock"
	// agentTokenDir holds the agent host's token in the agent container:
	// outside every session's read rules.
	agentTokenDir = "/run/apo-token"
)

// relayPort is where the relay listens in the pod.
const relayPort = 7071

// uid/gid the pod runs as (root only inside its own user namespace, and only
// for the tunnel).
const podUser = 1000

const (
	labelManaged   = "apothecary.io/managed"
	labelAssistant = "apothecary.io/assistant"
	annoPerson     = "apothecary.io/person"
	annoLastUsed   = "apothecary.io/last-used"
)

// personKey is a person's name on the cluster: their account id, lowercased.
func personKey(person string) string { return envport.ProjectHostKey(person) }

func podName(person string) string    { return "apo-assistant-" + personKey(person) }
func volumeName(person string) string { return podName(person) }
func secretName(person string) string { return podName(person) + "-relay" }

// gatewayKey is the person's gateway's key: the egress gateway manager's
// "project" (apo-gw-assistant-<key>), never a workshop's id — nothing that
// walks workshops (the egress reaper) ever names it.
func gatewayKey(person string) string { return "assistant-" + personKey(person) }

// gatewayPeer is the pod's peer on that gateway (its WireGuard Secret).
func gatewayPeer(person string) string { return "assistant-" + personKey(person) }

// Folder is where a repository lives in the pod:
// /workshops/<workshop name>-<hash>/<repository>-<hash>. The names are for
// people; the hashes (of the workshop's id, and of it with the
// repository's name) keep two workshops or two repositories whose names
// read the same in a folder from ever sharing one.
func Folder(r assistantport.Repo) string {
	name := slug(r.Name)
	if name == "" {
		name = "repo"
	}
	return path.Join(WorkshopFolder(r), name+"-"+shortHash(r.Workshop+"\x00"+r.Name))
}

// shortHash is 8 hex digits of s's SHA-256.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

// WorkshopFolder is a workshop's folder: a session's working directory.
func WorkshopFolder(r assistantport.Repo) string {
	ws := slug(r.WorkshopName)
	if ws == "" {
		ws = "workshop"
	}
	return path.Join(reposRoot, ws+"-"+shortHash(r.Workshop))
}

// Home is a session's state folder: its harness's home.
func Home(sessionID string) string { return path.Join(stateRoot, slug(sessionID)) }

// slug keeps a name to what a path segment holds safely.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.', r == ' ', r == '/':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

// Config is how persons' pods are made.
type Config struct {
	Kube *kube.Client
	// Image is the assistant image (workspace/images/assistant); ShimImage
	// the shim's (seeded, and the relay); ToolsImage the harness bundle's
	// (seeded); TunnelImage the gateway image, whose ip and wg bring the
	// tunnel up.
	Image, ShimImage, ToolsImage, TunnelImage string
	PullPolicy                                string
	// StorageClass and VolumeSize make each person's volume; "" class is the
	// cluster's default.
	StorageClass, VolumeSize string
	// Resources are the agent container's; nil: none set.
	Resources *kube.ResourceRequirements
	// EngineNamespace selects the engine's pods in the network policy;
	// EngineCIDR replaces that when the engine runs outside the cluster.
	// EnginePort is the engine's API port (its MCP face).
	EngineNamespace, EngineCIDR string
	EnginePort                  int
	// SessionIdle stops a session's process no turn has used for this long.
	SessionIdle time.Duration
	// Forward reaches the relay through a port-forward (an engine outside
	// the cluster) instead of the pod's IP.
	Forward bool
}

// tunnel is the pod's link to the person's gateway.
type tunnel struct {
	GatewayIP  string // the gateway as the pod reaches it: its resolver
	Endpoint   string // host:port of the gateway's WireGuard
	EndpointIP string // that host alone
	Secret     string // the Secret holding the pod's WireGuard identity
	EngineIP   string // the engine's address, routed outside the tunnel
}

func ptr[T any](v T) *T { return &v }

// tunnelScript brings the WireGuard link up, then watches it: the sandbox's
// apo-wg-up without the sysctls a non-privileged container cannot write and
// this pod does not need (docs/notes/2026-11-19-assistant-pod-experiments.md).
// It runs in the tunnel container alone; the agent has no shell.
const tunnelScript = `set -eu
ORIG_GW=$(ip -4 route show default | awk '/via/ {print $3; exit}')
[ -n "$ORIG_GW" ] || { echo "tunnel: no default gateway" >&2; exit 1; }
LINK_MTU=$(ip -o link show dev eth0 | awk '{for (i = 1; i <= NF; i++) if ($i == "mtu") {print $(i + 1); exit}}')
MTU=$(( ${LINK_MTU:-1500} - 60 ))
ip link show wg0 >/dev/null 2>&1 || ip link add wg0 type wireguard
cat /secrets/wg/wg-private-key > /etc/wireguard/wg0.key
wg set wg0 private-key /etc/wireguard/wg0.key \
  peer "$(cat /secrets/wg/gw-public-key)" endpoint "$GW_ENDPOINT" allowed-ips 0.0.0.0/0 persistent-keepalive 25
ip address replace "$(cat /secrets/wg/wg-ip)/32" dev wg0
ip link set wg0 mtu "$MTU" up
ip route replace "$GW_SVC_IP/32" via "$ORIG_GW" dev eth0
ip route replace "$ENGINE_IP/32" via "$ORIG_GW" dev eth0
ip route replace default dev wg0
touch /run/apo-net/ready
echo "tunnel: wg0 up through $GW_ENDPOINT (mtu $MTU)"
while sleep 60; do
  hs=$(wg show wg0 latest-handshakes | awk '{print $2; exit}')
  if [ $(( $(date +%s) - ${hs:-0} )) -gt 180 ]; then wg set wg0 listen-port 0 || true; fi
done`

// toolsSeed copies the harnesses the assistant runs from the tools image
// onto the person's volume, skipped when the volume already holds this
// bundle (its TOOLS_HASH): a copy beside, then a swap, as seed.sh does.
const toolsSeed = `set -eu
d=/seed/tools
want=$(cat /tools/TOOLS_HASH)
[ "$(cat $d/TOOLS_HASH 2>/dev/null)" = "$want" ] && exit 0
rm -rf $d.tmp $d.old
mkdir -p $d.tmp/musl/bin $d.tmp/musl/libexec
cp -a /tools/musl/lib $d.tmp/musl/lib
for f in claude claude-agent-acp opencode; do cp -a /tools/musl/libexec/$f $d.tmp/musl/libexec/$f; done
echo "$want" > $d.tmp/TOOLS_HASH
[ ! -e $d ] || mv $d $d.old
mv $d.tmp $d
rm -rf $d.old`

// locked is the security context every container but the tunnel runs with.
func locked() *kube.SecurityContext {
	return &kube.SecurityContext{
		AllowPrivilegeEscalation: ptr(false),
		Capabilities:             &kube.Capabilities{Drop: []string{"ALL"}},
		RunAsNonRoot:             ptr(true),
		RunAsUser:                ptr(int64(podUser)),
		RunAsGroup:               ptr(int64(podUser)),
		ReadOnlyRootFilesystem:   ptr(true),
		SeccompProfile:           &kube.SeccompProfile{Type: "RuntimeDefault"},
	}
}

func (c Config) labels(person string) map[string]string {
	return map[string]string{labelManaged: "true", labelAssistant: personKey(person)}
}

// podLabels add what the person's gateway admits a tunnel from: its key as
// the project, and the peer mark.
func (c Config) podLabels(person string) map[string]string {
	l := c.labels(person)
	l["apothecary.io/project"] = envport.ProjectHostKey(gatewayKey(person))
	l[egressgw.LabelGatewayPeer] = "true"
	return l
}

// podManifest is a person's pod.
func (c Config) podManifest(person string, tn tunnel) kube.Pod {
	idle := c.SessionIdle
	if idle <= 0 {
		idle = 10 * time.Minute
	}
	repos := kube.VolumeMount{Name: "data", SubPath: "repos", MountPath: reposRoot}
	reposRO := repos
	reposRO.ReadOnly = true
	sock := kube.VolumeMount{Name: "sock", MountPath: sockDir}
	shimRO := kube.VolumeMount{Name: "shim", MountPath: shimDir, ReadOnly: true}
	toolsRO := kube.VolumeMount{Name: "data", SubPath: "seed/tools", MountPath: toolsDir, ReadOnly: true}
	tunnelSec := &kube.SecurityContext{
		// Root inside the pod's user namespace, which owns the pod's network:
		// NET_ADMIN there is the pod's network alone.
		AllowPrivilegeEscalation: ptr(false),
		Capabilities:             &kube.Capabilities{Drop: []string{"ALL"}, Add: []string{"NET_ADMIN"}},
		RunAsNonRoot:             ptr(false),
		RunAsUser:                ptr(int64(0)),
		ReadOnlyRootFilesystem:   ptr(true),
		SeccompProfile:           &kube.SeccompProfile{Type: "RuntimeDefault"},
	}
	secretMode := int32(0o440)
	ns := ""
	if c.Kube != nil {
		ns = c.Kube.Namespace
	}
	return kube.Pod{TypeMeta: kube.PodType(),
		Metadata: kube.ObjectMeta{Name: podName(person), Labels: c.podLabels(person),
			Annotations: map[string]string{annoPerson: person}},
		Spec: kube.PodSpec{
			RestartPolicy:                 "Always",
			TerminationGracePeriodSeconds: ptr(int64(5)),
			HostUsers:                     ptr(false),
			AutomountServiceAccountToken:  ptr(false),
			EnableServiceLinks:            ptr(false),
			// Names resolve at the gateway, through the tunnel.
			DNSPolicy: "None",
			DNSConfig: &kube.PodDNSConfig{
				Nameservers: []string{tn.GatewayIP},
				Searches:    []string{ns + ".svc.cluster.local", "svc.cluster.local", "cluster.local"},
				Options:     []kube.PodDNSConfigOpt{{Name: "ndots", Value: "2"}},
			},
			SecurityContext: &kube.PodSecurityContext{
				RunAsNonRoot:   ptr(true),
				RunAsUser:      ptr(int64(podUser)),
				RunAsGroup:     ptr(int64(podUser)),
				FSGroup:        ptr(int64(podUser)),
				SeccompProfile: &kube.SeccompProfile{Type: "RuntimeDefault"},
			},
			InitContainers: []kube.Container{
				{Name: "shim-seed", Image: c.ShimImage, ImagePullPolicy: c.PullPolicy,
					Args:            []string{"seed", shimDir},
					SecurityContext: locked(),
					VolumeMounts:    []kube.VolumeMount{{Name: "shim", MountPath: shimDir}}},
				{Name: "tools-seed", Image: c.ToolsImage, ImagePullPolicy: c.PullPolicy,
					Command:         []string{"/bin/sh", "-c", toolsSeed},
					SecurityContext: locked(),
					VolumeMounts:    []kube.VolumeMount{{Name: "data", SubPath: "seed", MountPath: "/seed"}}},
				{Name: tunnelContainer, Image: c.TunnelImage, ImagePullPolicy: c.PullPolicy,
					RestartPolicy: "Always",
					Command:       []string{"/bin/sh", "-c", tunnelScript},
					Env: []kube.EnvVar{
						{Name: "GW_ENDPOINT", Value: tn.Endpoint},
						{Name: "GW_SVC_IP", Value: tn.EndpointIP},
						{Name: "ENGINE_IP", Value: tn.EngineIP},
					},
					SecurityContext: tunnelSec,
					StartupProbe: &kube.Probe{Exec: &kube.ExecAction{Command: []string{"test", "-f", netDir + "/ready"}},
						PeriodSeconds: 1, FailureThreshold: 60},
					VolumeMounts: []kube.VolumeMount{
						{Name: "wg-secret", MountPath: "/secrets/wg", ReadOnly: true},
						{Name: "wg-keys", MountPath: "/etc/wireguard"},
						{Name: "net", MountPath: netDir},
					}},
				{Name: relayContainer, Image: c.ShimImage, ImagePullPolicy: c.PullPolicy,
					RestartPolicy: "Always",
					Args: []string{"relay", "--serve", fmt.Sprintf("0.0.0.0:%d", relayPort),
						"--dir", sockDir, "--token-file", "/secrets/relay/relay-token"},
					Ports:           []kube.ContainerPort{{Name: "relay", ContainerPort: relayPort}},
					SecurityContext: locked(),
					VolumeMounts: []kube.VolumeMount{sock,
						{Name: "relay-secret", MountPath: "/secrets/relay", ReadOnly: true}}},
			},
			Containers: []kube.Container{
				{Name: agentContainer, Image: c.Image, ImagePullPolicy: c.PullPolicy,
					Command: []string{shimBin, "sidecar", "--listen", path.Join(sockDir, agentSock),
						"--repo", reposRoot, "--repos", reposRoot, "--ssh", "off",
						"--agent-idle", idle.String(), "--write-root", stateRoot,
						// Its API takes the relay's token, from a folder no
						// session's rules let it read, and starts confined
						// processes only.
						"--token-file", agentTokenDir + "/relay-token", "--confined-only"},
					Env:             []kube.EnvVar{{Name: "HOME", Value: stateRoot}},
					SecurityContext: locked(),
					Resources:       c.Resources,
					VolumeMounts: []kube.VolumeMount{reposRO, sock, shimRO, toolsRO,
						{Name: "data", SubPath: "state", MountPath: stateRoot},
						{Name: "relay-secret", MountPath: agentTokenDir, ReadOnly: true}}},
				{Name: gitContainer, Image: c.Image, ImagePullPolicy: c.PullPolicy,
					Command:         []string{shimBin, "idle"},
					SecurityContext: locked(),
					VolumeMounts:    []kube.VolumeMount{repos, shimRO}},
			},
			Volumes: []kube.Volume{
				{Name: "data", PersistentVolumeClaim: &kube.PersistentVolumeClaimVolumeSource{ClaimName: volumeName(person)}},
				{Name: "sock", EmptyDir: &kube.EmptyDirVolumeSource{Medium: "Memory", SizeLimit: "1Mi"}},
				{Name: "shim", EmptyDir: &kube.EmptyDirVolumeSource{SizeLimit: "64Mi"}},
				{Name: "net", EmptyDir: &kube.EmptyDirVolumeSource{Medium: "Memory", SizeLimit: "1Mi"}},
				{Name: "wg-keys", EmptyDir: &kube.EmptyDirVolumeSource{Medium: "Memory", SizeLimit: "1Mi"}},
				{Name: "wg-secret", Secret: &kube.SecretVolumeSource{SecretName: tn.Secret, DefaultMode: &secretMode}},
				{Name: "relay-secret", Secret: &kube.SecretVolumeSource{SecretName: secretName(person), DefaultMode: &secretMode}},
			},
		}}
}

// volumeManifest is a person's volume: the clones and the sessions' state.
func (c Config) volumeManifest(person string, now time.Time) kube.PersistentVolumeClaim {
	size := c.VolumeSize
	if size == "" {
		size = "10Gi" // the harnesses take 0.6 GB of it
	}
	pvc := kube.PersistentVolumeClaim{TypeMeta: kube.PVCType(),
		Metadata: kube.ObjectMeta{Name: volumeName(person), Labels: c.labels(person),
			Annotations: map[string]string{annoPerson: person, annoLastUsed: now.UTC().Format(time.RFC3339)}},
		Spec: kube.PVCSpec{AccessModes: []string{"ReadWriteOnce"},
			Resources: kube.ResourceRequirements{Requests: map[string]string{"storage": size}}}}
	if c.StorageClass != "" {
		pvc.Spec.StorageClassName = ptr(c.StorageClass)
	}
	return pvc
}

// networkPolicy: in, the engine to the relay; out, the tunnel to the
// person's gateway and the engine. Nothing else: everything the agent
// sends leaves through the gateway's filter.
func (c Config) networkPolicy(person string) kube.NetworkPolicy {
	engine := kube.NetworkPolicyPeer{PodSelector: &kube.LabelSelector{MatchLabels: map[string]string{egressgw.EngineAppLabel: egressgw.EngineAppValue}}}
	if c.EngineNamespace != "" {
		engine.NamespaceSelector = kube.NamespaceSelector(c.EngineNamespace)
	}
	if c.EngineCIDR != "" {
		engine = kube.NetworkPolicyPeer{IPBlock: &kube.IPBlock{CIDR: c.EngineCIDR}}
	}
	port := c.EnginePort
	if port == 0 {
		port = 8080
	}
	return kube.NetworkPolicy{TypeMeta: kube.NetworkPolicyType(),
		Metadata: kube.ObjectMeta{Name: podName(person), Labels: c.labels(person)},
		Spec: kube.NetworkPolicySpec{
			PodSelector: kube.LabelSelector{MatchLabels: map[string]string{labelAssistant: personKey(person)}},
			PolicyTypes: []string{"Ingress", "Egress"},
			Ingress: []kube.NetworkPolicyRule{{From: []kube.NetworkPolicyPeer{engine},
				Ports: []kube.NetworkPolicyPort{{Protocol: "TCP", Port: relayPort}}}},
			Egress: []kube.NetworkPolicyRule{
				{To: []kube.NetworkPolicyPeer{{PodSelector: &kube.LabelSelector{MatchLabels: map[string]string{"apothecary.io/gateway": envport.ProjectHostKey(gatewayKey(person))}}}},
					Ports: []kube.NetworkPolicyPort{{Protocol: "UDP", Port: 51820}}},
				{To: []kube.NetworkPolicyPeer{engine},
					Ports: []kube.NetworkPolicyPort{{Protocol: "TCP", Port: port}}},
			},
		}}
}

// relaySecret holds the pod's relay token.
func (c Config) relaySecret(person, token string) kube.Secret {
	return kube.Secret{TypeMeta: kube.SecretType(),
		Metadata:   kube.ObjectMeta{Name: secretName(person), Labels: c.labels(person)},
		StringData: map[string]string{"relay-token": token}}
}
