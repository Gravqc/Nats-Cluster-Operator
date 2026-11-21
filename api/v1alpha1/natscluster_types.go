package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NatsClusterSpec describes an independent NATS cluster.
// +kubebuilder:validation:XValidation:rule="self.replicas == oldSelf.replicas",message="Scaling is not supported in v0.1."
// +kubebuilder:validation:XValidation:rule="self.storage == oldSelf.storage",message="Storage resize requires a future lifecycle operation."
// +kubebuilder:validation:XValidation:rule="self.credentialsSecret == oldSelf.credentialsSecret",message="Changing credentials is not supported in v0.1."
// +kubebuilder:validation:XValidation:rule="self.resources == oldSelf.resources",message="Resource changes are not supported in v0.1."
// +kubebuilder:validation:XValidation:rule="has(self.storageClassName) == has(oldSelf.storageClassName) && (!has(self.storageClassName) || self.storageClassName == oldSelf.storageClassName)",message="Storage class is immutable."
// +kubebuilder:validation:XValidation:rule="has(self.nodeSelector) == has(oldSelf.nodeSelector) && (!has(self.nodeSelector) || self.nodeSelector == oldSelf.nodeSelector)",message="Node placement is immutable in v0.1."
// +kubebuilder:validation:XValidation:rule="has(self.tolerations) == has(oldSelf.tolerations) && (!has(self.tolerations) || self.tolerations == oldSelf.tolerations)",message="Tolerations are immutable in v0.1."
// +kubebuilder:validation:XValidation:rule="self.version.split('.')[0] == oldSelf.version.split('.')[0] && self.version.split('.')[1] == oldSelf.version.split('.')[1] && int(self.version.split('.')[2]) >= int(oldSelf.version.split('.')[2])",message="v0.1 supports forward patch upgrades within one minor release only."
type NatsClusterSpec struct {
	// PodMonitor creates a per-cluster Prometheus PodMonitor when its CRD is installed.
	// +optional
	// +kubebuilder:default=false
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Monitoring integration is immutable in v0.1."
	PodMonitor bool `json:"podMonitor,omitempty"`
	// Version pins an exact upstream nats-server image tag.
	// +kubebuilder:validation:Pattern=`^2\.[0-9]+\.[0-9]+$`
	Version string `json:"version"`
	// +kubebuilder:validation:Enum=1;3;5
	Replicas int32 `json:"replicas"`
	// CredentialsSecret must contain a nonempty token key in this namespace.
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
