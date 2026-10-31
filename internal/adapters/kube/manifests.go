package kube

// Just enough of the Kubernetes object model for the manifests apothecary
// writes — plain JSON-tagged structs, no apimachinery. Fields are added as a
// manifest needs them; everything is omitempty so an unset field means "the
// server's default".

type TypeMeta struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
}

type ObjectMeta struct {
	Name        string            `json:"name,omitempty"`
	Namespace   string            `json:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type LabelSelector struct {
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

// --- core/v1 -----------------------------------------------------------------

type Pod struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta `json:"metadata"`
	Spec     PodSpec    `json:"spec"`
	Status   PodStatus  `json:"status,omitempty"`
}

type PodSpec struct {
	RestartPolicy                 string              `json:"restartPolicy,omitempty"`
	TerminationGracePeriodSeconds *int64              `json:"terminationGracePeriodSeconds,omitempty"`
	RuntimeClassName              string              `json:"runtimeClassName,omitempty"`
	ServiceAccountName            string              `json:"serviceAccountName,omitempty"`
	AutomountServiceAccountToken  *bool               `json:"automountServiceAccountToken,omitempty"`
	EnableServiceLinks            *bool               `json:"enableServiceLinks,omitempty"`
	DNSPolicy                     string              `json:"dnsPolicy,omitempty"`
	DNSConfig                     *PodDNSConfig       `json:"dnsConfig,omitempty"`
	Hostname                      string              `json:"hostname,omitempty"`
	InitContainers                []Container         `json:"initContainers,omitempty"`
	Containers                    []Container         `json:"containers"`
	Volumes                       []Volume            `json:"volumes,omitempty"`
	NodeSelector                  map[string]string   `json:"nodeSelector,omitempty"`
	SecurityContext               *PodSecurityContext `json:"securityContext,omitempty"`
}

type PodSecurityContext struct {
	FSGroup *int64 `json:"fsGroup,omitempty"`
}

type PodDNSConfig struct {
	Nameservers []string          `json:"nameservers,omitempty"`
	Searches    []string          `json:"searches,omitempty"`
	Options     []PodDNSConfigOpt `json:"options,omitempty"`
}

type PodDNSConfigOpt struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

type Container struct {
	Name            string                `json:"name"`
	Image           string                `json:"image"`
	ImagePullPolicy string                `json:"imagePullPolicy,omitempty"`
	Command         []string              `json:"command,omitempty"`
	Args            []string              `json:"args,omitempty"`
	Env             []EnvVar              `json:"env,omitempty"`
	Ports           []ContainerPort       `json:"ports,omitempty"`
	VolumeMounts    []VolumeMount         `json:"volumeMounts,omitempty"`
	SecurityContext *SecurityContext      `json:"securityContext,omitempty"`
	ReadinessProbe  *Probe                `json:"readinessProbe,omitempty"`
	StartupProbe    *Probe                `json:"startupProbe,omitempty"`
	LivenessProbe   *Probe                `json:"livenessProbe,omitempty"`
	RestartPolicy   string                `json:"restartPolicy,omitempty"` // "Always" on an init container = native sidecar
	Resources       *ResourceRequirements `json:"resources,omitempty"`
}

type ResourceRequirements struct {
	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
}

type EnvVar struct {
	Name      string        `json:"name"`
	Value     string        `json:"value,omitempty"`
	ValueFrom *EnvVarSource `json:"valueFrom,omitempty"`
}

type EnvVarSource struct {
	SecretKeyRef *SecretKeySelector `json:"secretKeyRef,omitempty"`
}

type SecretKeySelector struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type ContainerPort struct {
	Name          string `json:"name,omitempty"`
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol,omitempty"`
}

type VolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	SubPath   string `json:"subPath,omitempty"`
	ReadOnly  bool   `json:"readOnly,omitempty"`
}

type SecurityContext struct {
	Privileged               *bool         `json:"privileged,omitempty"`
	AllowPrivilegeEscalation *bool         `json:"allowPrivilegeEscalation,omitempty"`
	Capabilities             *Capabilities `json:"capabilities,omitempty"`
	RunAsUser                *int64        `json:"runAsUser,omitempty"`
}

type Capabilities struct {
	Add  []string `json:"add,omitempty"`
	Drop []string `json:"drop,omitempty"`
}

type Probe struct {
	Exec                *ExecAction    `json:"exec,omitempty"`
	HTTPGet             *HTTPGetAction `json:"httpGet,omitempty"`
	TCPSocket           *TCPSocket     `json:"tcpSocket,omitempty"`
	InitialDelaySeconds int            `json:"initialDelaySeconds,omitempty"`
	PeriodSeconds       int            `json:"periodSeconds,omitempty"`
	TimeoutSeconds      int            `json:"timeoutSeconds,omitempty"`
	FailureThreshold    int            `json:"failureThreshold,omitempty"`
}

type ExecAction struct {
	Command []string `json:"command"`
}

type HTTPGetAction struct {
	Path string `json:"path"`
	Port int    `json:"port"`
}

type TCPSocket struct {
	Port int `json:"port"`
}

type Volume struct {
	Name                  string                             `json:"name"`
	EmptyDir              *EmptyDirVolumeSource              `json:"emptyDir,omitempty"`
	PersistentVolumeClaim *PersistentVolumeClaimVolumeSource `json:"persistentVolumeClaim,omitempty"`
	Secret                *SecretVolumeSource                `json:"secret,omitempty"`
}

type EmptyDirVolumeSource struct {
	Medium    string `json:"medium,omitempty"`
	SizeLimit string `json:"sizeLimit,omitempty"`
}

type PersistentVolumeClaimVolumeSource struct {
	ClaimName string `json:"claimName"`
}

type SecretVolumeSource struct {
	SecretName  string `json:"secretName"`
	DefaultMode *int32 `json:"defaultMode,omitempty"`
}

type PodStatus struct {
	Phase             string            `json:"phase,omitempty"`
	PodIP             string            `json:"podIP,omitempty"`
	Conditions        []PodCondition    `json:"conditions,omitempty"`
	ContainerStatuses []ContainerStatus `json:"containerStatuses,omitempty"`
}

type PodCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type ContainerStatus struct {
	Name         string `json:"name"`
	Ready        bool   `json:"ready"`
	RestartCount int    `json:"restartCount"`
}

// Ready reports the pod's Ready condition.
func (s PodStatus) Ready() bool {
	for _, c := range s.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

type PersistentVolumeClaim struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta `json:"metadata"`
	Spec     PVCSpec    `json:"spec"`
}

type PVCSpec struct {
	AccessModes      []string             `json:"accessModes"`
	StorageClassName *string              `json:"storageClassName,omitempty"`
	Resources        ResourceRequirements `json:"resources"`
}

type Secret struct {
	TypeMeta   `json:",inline"`
	Metadata   ObjectMeta        `json:"metadata"`
	Type       string            `json:"type,omitempty"`
	StringData map[string]string `json:"stringData,omitempty"`
	Data       map[string][]byte `json:"data,omitempty"` // base64 on the wire
}

type Service struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta  `json:"metadata"`
	Spec     ServiceSpec `json:"spec"`
}

type ServiceSpec struct {
	Selector  map[string]string `json:"selector,omitempty"`
	Ports     []ServicePort     `json:"ports"`
	ClusterIP string            `json:"clusterIP,omitempty"`
	Type      string            `json:"type,omitempty"`
}

type ServicePort struct {
	Name       string `json:"name,omitempty"`
	Port       int    `json:"port"`
	TargetPort int    `json:"targetPort,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
}

// --- apps/v1 -----------------------------------------------------------------

type Deployment struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta     `json:"metadata"`
	Spec     DeploymentSpec `json:"spec"`
}

type DeploymentSpec struct {
	Replicas *int32              `json:"replicas,omitempty"`
	Strategy *DeploymentStrategy `json:"strategy,omitempty"`
	Selector LabelSelector       `json:"selector"`
	Template PodTemplateSpec     `json:"template"`
}

type DeploymentStrategy struct {
	Type string `json:"type"`
}

type PodTemplateSpec struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     PodSpec    `json:"spec"`
}

// --- networking.k8s.io/v1 ------------------------------------------------------

type NetworkPolicy struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta        `json:"metadata"`
	Spec     NetworkPolicySpec `json:"spec"`
}

type NetworkPolicySpec struct {
	PodSelector LabelSelector       `json:"podSelector"`
	PolicyTypes []string            `json:"policyTypes"`
	Ingress     []NetworkPolicyRule `json:"ingress,omitempty"`
	Egress      []NetworkPolicyRule `json:"egress,omitempty"`
}

// NetworkPolicyRule is an ingress (From) or egress (To) rule.
type NetworkPolicyRule struct {
	From  []NetworkPolicyPeer `json:"from,omitempty"`
	To    []NetworkPolicyPeer `json:"to,omitempty"`
	Ports []NetworkPolicyPort `json:"ports,omitempty"`
}

type NetworkPolicyPeer struct {
	PodSelector       *LabelSelector `json:"podSelector,omitempty"`
	NamespaceSelector *LabelSelector `json:"namespaceSelector,omitempty"`
	IPBlock           *IPBlock       `json:"ipBlock,omitempty"`
}

type IPBlock struct {
	CIDR   string   `json:"cidr"`
	Except []string `json:"except,omitempty"`
}

type NetworkPolicyPort struct {
	Protocol string `json:"protocol,omitempty"`
	Port     int    `json:"port,omitempty"`
}

// --- helpers -------------------------------------------------------------------

func Ptr[T any](v T) *T { return &v }

func PodType() TypeMeta        { return TypeMeta{APIVersion: "v1", Kind: "Pod"} }
func PVCType() TypeMeta        { return TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"} }
func SecretType() TypeMeta     { return TypeMeta{APIVersion: "v1", Kind: "Secret"} }
func ServiceType() TypeMeta    { return TypeMeta{APIVersion: "v1", Kind: "Service"} }
func DeploymentType() TypeMeta { return TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"} }
func NetworkPolicyType() TypeMeta {
	return TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"}
}

// NamespaceSelector selects one namespace by name (every namespace carries
// the kubernetes.io/metadata.name label).
func NamespaceSelector(ns string) *LabelSelector {
	return &LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": ns}}
}
