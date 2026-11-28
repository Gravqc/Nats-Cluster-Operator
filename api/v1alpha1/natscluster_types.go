package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NatsClusterSpec describes an independent NATS cluster.
// +kubebuilder:validation:XValidation:rule="has(self.goMemoryLimitPercent) == has(oldSelf.goMemoryLimitPercent) && (!has(self.goMemoryLimitPercent) || self.goMemoryLimitPercent == oldSelf.goMemoryLimitPercent)",message="Runtime memory policy is immutable."
// +kubebuilder:validation:XValidation:rule="has(self.goGCPercent) == has(oldSelf.goGCPercent) && (!has(self.goGCPercent) || self.goGCPercent == oldSelf.goGCPercent)",message="Runtime GC policy is immutable."
// +kubebuilder:validation:XValidation:rule="!has(self.goMemoryLimitPercent) || (has(self.resources.limits) && 'memory' in self.resources.limits)",message="goMemoryLimitPercent requires a container memory limit."
// +kubebuilder:validation:XValidation:rule="self.replicas == oldSelf.replicas",message="Scaling is not supported in v0.1."
// +kubebuilder:validation:XValidation:rule="self.storage == oldSelf.storage",message="Storage resize requires a future lifecycle operation."
// +kubebuilder:validation:XValidation:rule="self.credentialsSecret == oldSelf.credentialsSecret",message="Changing credentials is not supported in v0.1."
// +kubebuilder:validation:XValidation:rule="self.resources == oldSelf.resources",message="Resource changes are not supported in v0.1."
// +kubebuilder:validation:XValidation:rule="has(self.storageClassName) == has(oldSelf.storageClassName) && (!has(self.storageClassName) || self.storageClassName == oldSelf.storageClassName)",message="Storage class is immutable."
// +kubebuilder:validation:XValidation:rule="has(self.nodeSelector) == has(oldSelf.nodeSelector) && (!has(self.nodeSelector) || self.nodeSelector == oldSelf.nodeSelector)",message="Node placement is immutable in v0.1."
// +kubebuilder:validation:XValidation:rule="has(self.tolerations) == has(oldSelf.tolerations) && (!has(self.tolerations) || self.tolerations == oldSelf.tolerations)",message="Tolerations are immutable in v0.1."
// +kubebuilder:validation:XValidation:rule="self.version == oldSelf.version || !self.version.matches('^2[.](0|[1-9][0-9]{0,8})[.](0|[1-9][0-9]{0,8})$') || !oldSelf.version.matches('^2[.](0|[1-9][0-9]{0,8})[.](0|[1-9][0-9]{0,8})$') || (self.version.split('.')[0] == oldSelf.version.split('.')[0] && ((self.version.split('.')[1] == oldSelf.version.split('.')[1] && int(self.version.split('.')[2]) >= int(oldSelf.version.split('.')[2])) || int(self.version.split('.')[1]) == int(oldSelf.version.split('.')[1]) + 1))",message="Only forward patch or adjacent-minor upgrades are supported."
type NatsClusterSpec struct {
	// GoMemoryLimitPercent sets each NATS process GOMEMLIMIT relative to its
	// container limit. This soft Go-runtime limit excludes Linux page cache.
	// +optional
	// +kubebuilder:validation:Minimum=50
	// +kubebuilder:validation:Maximum=95
	GoMemoryLimitPercent *int32 `json:"goMemoryLimitPercent,omitempty"`
	// GoGCPercent controls heap growth between collections, not physical memory
	// utilization. A lower value trades additional CPU for a smaller heap.
	// +optional
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:validation:Maximum=500
	GoGCPercent *int32 `json:"goGCPercent,omitempty"`

	// PodMonitor creates a per-cluster Prometheus PodMonitor when its CRD is installed.
	// +optional
	// +kubebuilder:default=false
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Monitoring integration is immutable in v0.1."
	PodMonitor bool `json:"podMonitor,omitempty"`
	// Version pins an exact upstream nats-server image tag.
	// +kubebuilder:validation:MaxLength=21
	// +kubebuilder:validation:Pattern=`^2\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`
	Version string `json:"version"`
	// +kubebuilder:validation:Enum=1;3
	Replicas int32 `json:"replicas"`
	// CredentialsSecret must contain a token key that NATS parses as unquoted text.
	// +kubebuilder:validation:MinLength=1
	CredentialsSecret string `json:"credentialsSecret"`
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*(Mi|Gi|Ti)$`
	Storage string `json:"storage"`
	// +optional
	StorageClassName *string                     `json:"storageClassName,omitempty"`
	Resources        corev1.ResourceRequirements `json:"resources"`
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

type ServerStatus struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Connections int    `json:"connections"`
	MemoryBytes int64  `json:"memoryBytes"`
	Healthy     bool   `json:"healthy"`
}

type NatsClusterStatus struct {
	// AppliedVersion is the last target observed healthy on every server.
	AppliedVersion string `json:"appliedVersion,omitempty"`

	// UpgradeVersion pins an in-progress target across controller restarts.
	UpgradeVersion string `json:"upgradeVersion,omitempty"`

	Servers            []ServerStatus `json:"servers,omitempty"`
	MetaLeader         string         `json:"metaLeader,omitempty"`
	HealthySince       *metav1.Time   `json:"healthySince,omitempty"`
	LastRestartedPod   string         `json:"lastRestartedPod,omitempty"`
	ObservedGeneration int64          `json:"observedGeneration,omitempty"`
	ClientURL          string         `json:"clientURL,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Servers",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.clientURL`
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 40",message="Cluster name must contain at most 40 characters."
// +kubebuilder:validation:XValidation:rule="oldSelf.hasValue() || self.metadata.name.matches('^[a-z]([-a-z0-9]*[a-z0-9])?$')",optionalOldSelf=true,message="Cluster name must be a DNS label beginning with a lowercase letter."
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.upgradeVersion) || self.spec.version == oldSelf.spec.version",message="Wait for the current upgrade to finish before changing version again."
// +kubebuilder:validation:XValidation:rule="self.spec.version == oldSelf.spec.version || !has(oldSelf.status) || !has(oldSelf.status.appliedVersion) || !self.spec.version.matches('^2[.](0|[1-9][0-9]{0,8})[.](0|[1-9][0-9]{0,8})$') || !oldSelf.status.appliedVersion.matches('^2[.](0|[1-9][0-9]{0,8})[.](0|[1-9][0-9]{0,8})$') || int(self.spec.version.split('.')[1]) <= int(oldSelf.status.appliedVersion.split('.')[1]) + 1",message="Do not skip a minor relative to the last healthy server version."
// +kubebuilder:validation:XValidation:rule="self.spec.version == oldSelf.spec.version || oldSelf.spec.version.matches('^2[.](0|[1-9][0-9]{0,8})[.](0|[1-9][0-9]{0,8})$') || !has(oldSelf.status) || !has(oldSelf.status.appliedVersion) || self.spec.version == oldSelf.status.appliedVersion",message="Restore the last healthy version before upgrading from a malformed version."
// NatsCluster provisions NATS servers. Application code owns stream policy.
type NatsCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              NatsClusterSpec `json:"spec"`
	// +optional
	Status NatsClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NatsClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NatsCluster `json:"items"`
}

func init() { SchemeBuilder.Register(&NatsCluster{}, &NatsClusterList{}) }
