package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	natsv1 "github.com/Gravqc/Nats-Cluster-Operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const finalizer = "nats.gravqc.github.io/retain-workloads"

var (
	tokenText         = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_+./=-]*$`)
	tokenNumberSuffix = regexp.MustCompile(`(?i)^[kmgtpe][bi]*$`)
)

// NATS parses an environment variable as configuration syntax. Restrict the
// token to unquoted text that the parser passes through unchanged.
func validToken(token []byte) bool {
	if !tokenText.Match(token) {
		return false
	}
	value := string(token)
	switch strings.ToLower(value) {
	case "true", "false", "on", "off", "yes", "no":
		return false
	}
	afterDigits := strings.TrimLeft(value, "0123456789")
	return afterDigits != "" && (len(afterDigits) == len(value) ||
		(!strings.HasPrefix(afterDigits, ".") && !strings.HasPrefix(afterDigits, "-") &&
			!tokenNumberSuffix.MatchString(afterDigits)))
}

func memoryLimitBytes(spec Spec) int64 {
	limit := spec.Resources.Limits.Memory().Value()
	percent := int64(*spec.GoMemoryLimitPercent)
	return limit/100*percent + limit%100*percent/100
}

type Spec = natsv1.NatsClusterSpec

type Reconciler struct{ client.Client }

func (r *Reconciler) Setup(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).For(&natsv1.NatsCluster{}, builder.WithPredicates(predicate.Funcs{
		UpdateFunc: func(update event.UpdateEvent) bool {
			return update.ObjectNew.GetGeneration() != update.ObjectOld.GetGeneration() ||
				(!update.ObjectNew.GetDeletionTimestamp().IsZero() && update.ObjectOld.GetDeletionTimestamp().IsZero())
		},
	})).
		Owns(&appsv1.StatefulSet{}).Owns(&corev1.Service{}).Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Pod{}).Owns(&policyv1.PodDisruptionBudget{}).Owns(&networkingv1.NetworkPolicy{}).Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	object := &natsv1.NatsCluster{}
	if err := r.Get(ctx, request.NamespacedName, object); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !object.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, r.retain(ctx, object)
	}
	if !controllerutil.ContainsFinalizer(object, finalizer) {
		before := object.DeepCopy()
		controllerutil.AddFinalizer(object, finalizer)
		if err := r.Patch(ctx, object, client.MergeFrom(before)); err != nil {
			return ctrl.Result{}, err
		}
	}
	spec := object.Spec
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: object.GetNamespace(), Name: spec.CredentialsSecret}, secret)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, r.status(ctx, object, false, "CredentialsMissing", "Create the credentials Secret with a nonempty token key")
	}
	if len(secret.Data["token"]) == 0 {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, r.status(ctx, object, false, "CredentialsMissing", "Credentials Secret requires a nonempty token key")
	}
	// Previously healthy clusters already passed startup with their credentials.
	// Keep those credentials grandfathered while rotation remains unsupported.
	if object.Status.AppliedVersion == "" && !validToken(secret.Data["token"]) {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, r.status(ctx, object, false, "CredentialsInvalid", "Use an unquoted text token such as nats_ followed by random hexadecimal characters")
	}
	if spec.GoMemoryLimitPercent != nil && memoryLimitBytes(spec) <= 0 {
		return ctrl.Result{}, r.status(ctx, object, false, "InvalidMemoryPolicy", "goMemoryLimitPercent requires a container memory limit that yields at least one byte")
	}
	// Freeze the version request until the current rollout finishes. Updating the
	// StatefulSet again could otherwise let a failed pod skip a server minor.
	if object.Status.UpgradeVersion != "" && object.Status.UpgradeVersion != spec.Version {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, r.status(ctx, object, false, "UpgradeTargetChanged", "The persisted rollout target differs from the request; administrator intervention is required")
	}
	existing := &appsv1.StatefulSet{}
	if err := r.Get(ctx, request.NamespacedName, existing); err == nil {
		oldImage := existing.Spec.Template.Spec.Containers[0].Image
		newImage := "nats:" + spec.Version + "-alpine"
		if oldImage != newImage && object.Status.UpgradeVersion == "" {
			if !forwardUpgrade(strings.TrimSuffix(strings.TrimPrefix(oldImage, "nats:"), "-alpine"), spec.Version) {
				return ctrl.Result{RequeueAfter: 15 * time.Second}, r.status(ctx, object, false, "UnsupportedUpgrade", "The running StatefulSet must upgrade forward by at most one server minor")
			}
			object.Status.UpgradeVersion = spec.Version
			return ctrl.Result{RequeueAfter: time.Second}, r.status(ctx, object, false, "UpgradePlanned", "Pinned the target before updating the StatefulSet")
		}
	} else if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	for _, desired := range Resources(object, spec) {
		if err = r.apply(ctx, object, desired); err != nil {
			statusErr := r.status(ctx, object, false, "ReconcileFailed", err.Error())
			if statusErr != nil {
				return ctrl.Result{}, statusErr
			}
			return ctrl.Result{}, err
		}
	}
	statefulSet := &appsv1.StatefulSet{}
	if err = r.Get(ctx, request.NamespacedName, statefulSet); err != nil {
		return ctrl.Result{}, err
	}
	healthy, reason := r.observe(ctx, object)
	if !healthy {
		object.Status.HealthySince = nil
		return ctrl.Result{RequeueAfter: 5 * time.Second}, r.status(ctx, object, false, "HealthGateBlocked", reason)
	}
	if object.Status.HealthySince == nil {
		now := metav1.Now()
		object.Status.HealthySince = &now
	}
	pods := &corev1.PodList{}
	if err = r.List(ctx, pods, client.InNamespace(object.Namespace), client.MatchingLabels(labelsFor(object.Name))); err != nil {
		return ctrl.Result{}, err
	}
	targetImage := "nats:" + object.Spec.Version + "-alpine"
	outdated := []corev1.Pod{}
	for _, pod := range pods.Items {
		if !pod.DeletionTimestamp.IsZero() || !podReady(pod) {
			object.Status.HealthySince = nil
			return ctrl.Result{RequeueAfter: 5 * time.Second}, r.status(ctx, object, false, "WaitingForPod", "Waiting for every pod to be running and Ready")
		}
		if pod.Spec.Containers[0].Image != targetImage {
			outdated = append(outdated, pod)
		}
	}
	if len(pods.Items) != int(spec.Replicas) {
		object.Status.HealthySince = nil
		return ctrl.Result{RequeueAfter: 5 * time.Second}, r.status(ctx, object, false, "WaitingForPod", "Waiting for the requested pod count")
	}
	if len(outdated) == 0 {
		object.Status.AppliedVersion = spec.Version
		object.Status.UpgradeVersion = ""
		return ctrl.Result{RequeueAfter: 15 * time.Second}, r.status(ctx, object, true, "Healthy", "Monitoring endpoints report healthy servers and replicas; application code owns stream policy")
	}
	// A health observation must survive across reconciles before a deliberate restart.
	if time.Since(object.Status.HealthySince.Time) < 10*time.Second {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, r.status(ctx, object, false, "Stabilizing", "Waiting for ten seconds of observed health before restarting one server")
	}
	if object.Status.UpgradeVersion == "" {
		object.Status.UpgradeVersion = spec.Version
		return ctrl.Result{RequeueAfter: time.Second}, r.status(ctx, object, false, "UpgradePlanned", "Pinned the rollout target before restarting any server")
	}
	orderUpgrade(outdated, object.Status.MetaLeader)
	target := outdated[0]
	object.Status.HealthySince = nil
	object.Status.LastRestartedPod = target.Name
	if err = r.status(ctx, object, false, "Upgrading", "Restarting "+target.Name+" after all monitoring health gates passed"); err != nil {
		return ctrl.Result{}, err
	}
	err = r.Delete(ctx, &target, client.Preconditions{UID: &target.UID})
	return ctrl.Result{RequeueAfter: 5 * time.Second}, client.IgnoreNotFound(err)
}

func (r *Reconciler) apply(ctx context.Context, parent *natsv1.NatsCluster, desired client.Object) error {
	existing := desired.DeepCopyObject().(client.Object)
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if apierrors.IsNotFound(err) {
		controller := true
		desired.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: natsv1.GroupVersion.String(), Kind: "NatsCluster", Name: parent.GetName(), UID: parent.GetUID(), Controller: &controller}})
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if !metav1.IsControlledBy(existing, parent) {
		return fmt.Errorf("resource %T %s already exists outside this NatsCluster; automatic adoption is disabled", existing, existing.GetName())
	}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, existing, func() error {
		switch current := existing.(type) {
		case *unstructured.Unstructured:
			current.Object["spec"] = desired.(*unstructured.Unstructured).Object["spec"]
		case *corev1.Service:
			wanted := desired.(*corev1.Service)
			current.Spec.Selector = wanted.Spec.Selector
			current.Spec.Ports = wanted.Spec.Ports
			current.Spec.PublishNotReadyAddresses = wanted.Spec.PublishNotReadyAddresses
		case *corev1.ConfigMap:
			current.Data = desired.(*corev1.ConfigMap).Data
		case *appsv1.StatefulSet:
			wanted := desired.(*appsv1.StatefulSet)
			current.Spec.Replicas = wanted.Spec.Replicas
			// Preserve API-defaulted pod fields. Spec changes other than version are rejected by the CRD.
			current.Spec.UpdateStrategy = wanted.Spec.UpdateStrategy
			for i := range current.Spec.Template.Spec.Containers {
				for _, container := range wanted.Spec.Template.Spec.Containers {
					if current.Spec.Template.Spec.Containers[i].Name == container.Name {
						currentContainer := &current.Spec.Template.Spec.Containers[i]
						currentContainer.Image = container.Image
						if container.Name == "nats" && parent.Spec.GoMemoryLimitPercent != nil {
							for j := range currentContainer.Env {
								if currentContainer.Env[j].Name == "GOMEMLIMIT" {
									currentContainer.Env[j].Value = fmt.Sprintf("%dB", memoryLimitBytes(parent.Spec))
								}
							}
						}
					}
				}
			}
		case *policyv1.PodDisruptionBudget:
			current.Spec = desired.(*policyv1.PodDisruptionBudget).Spec
		case *networkingv1.NetworkPolicy:
			current.Spec = desired.(*networkingv1.NetworkPolicy).Spec
		}
		return nil
	})
	return err
}

func (r *Reconciler) status(ctx context.Context, object *natsv1.NatsCluster, ready bool, reason, message string) error {
	state := metav1.ConditionFalse
	if ready {
		state = metav1.ConditionTrue
	}
	object.Status.ObservedGeneration = object.Generation
	object.Status.ClientURL = fmt.Sprintf("nats://%s.%s.svc:4222", object.Name, object.Namespace)
	meta.SetStatusCondition(&object.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: state, Reason: reason, Message: message, ObservedGeneration: object.Generation,
	})
	return r.Status().Update(ctx, object)
}

func (r *Reconciler) retain(ctx context.Context, parent *natsv1.NatsCluster) error {
	if !controllerutil.ContainsFinalizer(parent, finalizer) {
		return nil
	}
	children := []client.Object{
		&appsv1.StatefulSet{}, &corev1.ConfigMap{}, &corev1.Service{}, &corev1.Service{},
		&policyv1.PodDisruptionBudget{}, &networkingv1.NetworkPolicy{},
	}
	if parent.Spec.PodMonitor {
		children = append(children, podMonitor(parent))
	}
	for index, child := range children {
		name := parent.GetName()
		if index == 3 {
			name += "-headless"
		}
		if err := r.Get(ctx, types.NamespacedName{Namespace: parent.GetNamespace(), Name: name}, child); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		if !metav1.IsControlledBy(child, parent) {
			continue
		}
		before := child.DeepCopyObject().(client.Object)
		owners := []metav1.OwnerReference{}
		for _, owner := range child.GetOwnerReferences() {
			if owner.UID != parent.GetUID() {
				owners = append(owners, owner)
			}
		}
		child.SetOwnerReferences(owners)
		if err := r.Patch(ctx, child, client.MergeFrom(before)); err != nil {
			return err
		}
	}
	before := parent.DeepCopy()
	controllerutil.RemoveFinalizer(parent, finalizer)
	return r.Patch(ctx, parent, client.MergeFrom(before))
}

func Resources(parent *natsv1.NatsCluster, spec Spec) []client.Object {
	name, namespace := parent.GetName(), parent.GetNamespace()
	labels := labelsFor(name)
	metadata := func(resourceName string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: resourceName, Namespace: namespace, Labels: labels}
	}
	routes := []string{}
	for ordinal := int32(0); ordinal < spec.Replicas; ordinal++ {
		routes = append(routes, fmt.Sprintf("nats-route://%s-%d.%s-headless.%s.svc:6222", name, ordinal, name, namespace))
	}
	routeJSON, _ := json.Marshal(routes)
	config := fmt.Sprintf(`server_name: $POD_NAME
listen: 0.0.0.0:4222
http: 0.0.0.0:8222
max_payload: 10485760
authorization { token: $NATS_AUTH_TOKEN }
jetstream { store_dir: /data/jetstream }
cluster {
  name: %q
  listen: 0.0.0.0:6222
  advertise: $ROUTE_ADDRESS
  routes: %s
}
`, name, routeJSON)
	if spec.Replicas == 1 {
		config = strings.Split(config, "cluster {\n")[0]
	}
	nonroot, noPrivilege := true, false
	user := int64(1000)
	grace := int64(120)
	disk := resource.MustParse(spec.Storage)
	servicePorts := []corev1.ServicePort{{Name: "client", Port: 4222, TargetPort: intstr.FromInt32(4222)}}
	headless := &corev1.Service{ObjectMeta: metadata(name + "-headless"), Spec: corev1.ServiceSpec{
		ClusterIP: "None", PublishNotReadyAddresses: true, Selector: labels,
		Ports: []corev1.ServicePort{{Name: "route", Port: 6222, TargetPort: intstr.FromInt32(6222)}},
	}}
	spread := &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{LabelSelector: &metav1.LabelSelector{MatchLabels: labels}, TopologyKey: "kubernetes.io/hostname"}},
	}}
	if spec.Replicas == 1 {
		spread = nil
	}
	statefulSet := &appsv1.StatefulSet{ObjectMeta: metadata(name), Spec: appsv1.StatefulSetSpec{
		PodManagementPolicy: appsv1.ParallelPodManagement, Replicas: &spec.Replicas, ServiceName: name + "-headless", Selector: &metav1.LabelSelector{MatchLabels: labels},
		UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
			AutomountServiceAccountToken: &noPrivilege,
			NodeSelector:                 spec.NodeSelector, Tolerations: spec.Tolerations, Affinity: spread,
			TerminationGracePeriodSeconds: &grace,
			SecurityContext:               &corev1.PodSecurityContext{RunAsNonRoot: &nonroot, RunAsUser: &user, RunAsGroup: &user, FSGroup: &user},
			Containers: []corev1.Container{{Name: "nats", Image: "nats:" + spec.Version + "-alpine", ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"nats-server"}, Args: []string{"--config", "/etc/nats/nats.conf"}, Resources: spec.Resources,
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &noPrivilege, ReadOnlyRootFilesystem: &nonroot, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				Env: []corev1.EnvVar{
					{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
					{Name: "ROUTE_ADDRESS", Value: fmt.Sprintf("$(POD_NAME).%s-headless.%s.svc:6222", name, namespace)},
					{Name: "NATS_AUTH_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: spec.CredentialsSecret}, Key: "token"}}},
				},
				Ports:          []corev1.ContainerPort{{Name: "client", ContainerPort: 4222}, {Name: "route", ContainerPort: 6222}, {Name: "monitor", ContainerPort: 8222}},
				VolumeMounts:   []corev1.VolumeMount{{Name: "config", MountPath: "/etc/nats", ReadOnly: true}, {Name: "data", MountPath: "/data"}, {Name: "tmp", MountPath: "/tmp"}},
				StartupProbe:   &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(8222)}}, PeriodSeconds: 2, TimeoutSeconds: 2, FailureThreshold: 150},
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(8222)}}, PeriodSeconds: 5, TimeoutSeconds: 2},
				Lifecycle:      &corev1.Lifecycle{PreStop: &corev1.LifecycleHandler{Exec: &corev1.ExecAction{Command: []string{"nats-server", "--signal", "ldm=1"}}}},
			}},
			Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}}, {Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
		}},
		VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "data"}, Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, StorageClassName: spec.StorageClassName,
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: disk}},
		}}},
	}}
	// Each broker runs its own collector. Never flush host caches or delete
	// JetStream files in response to memory pressure.
	if spec.GoMemoryLimitPercent != nil {
		statefulSet.Spec.Template.Spec.Containers[0].Env = append(statefulSet.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "GOMEMLIMIT", Value: fmt.Sprintf("%dB", memoryLimitBytes(spec))})
	}
	if spec.GoGCPercent != nil {
		statefulSet.Spec.Template.Spec.Containers[0].Env = append(statefulSet.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "GOGC", Value: fmt.Sprint(*spec.GoGCPercent)})
	}
	statefulSet.Spec.Template.Spec.Containers = append(statefulSet.Spec.Template.Spec.Containers, corev1.Container{
		Name: "metrics", Image: "natsio/prometheus-nats-exporter:0.20.2",
		Args:            []string{"-varz", "-connz", "-jsz=all", "http://localhost:8222"},
		Ports:           []corev1.ContainerPort{{Name: "metrics", ContainerPort: 7777}},
		Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("25m"), corev1.ResourceMemory: resource.MustParse("32Mi")}, Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")}},
		SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &noPrivilege, ReadOnlyRootFilesystem: &nonroot, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
	})
	unavailable := intstr.FromInt32(1)
	if spec.Replicas == 1 {
		unavailable = intstr.FromInt32(0)
	}
	port := func(number int32) networkingv1.NetworkPolicyPort {
		value := intstr.FromInt32(number)
		return networkingv1.NetworkPolicyPort{Port: &value}
	}
	network := &networkingv1.NetworkPolicy{ObjectMeta: metadata(name), Spec: networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: labels}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		Ingress: []networkingv1.NetworkPolicyIngressRule{
			{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}, Ports: []networkingv1.NetworkPolicyPort{port(4222)}},
			{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: labels}}}, Ports: []networkingv1.NetworkPolicyPort{port(6222)}},
			{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "nats-cluster-operator"}}}}, Ports: []networkingv1.NetworkPolicyPort{port(8222)}},
			{Ports: []networkingv1.NetworkPolicyPort{port(7777)}},
		},
	}}
	objects := []client.Object{
		&corev1.ConfigMap{ObjectMeta: metadata(name), Data: map[string]string{"nats.conf": config}},
		headless,
		&corev1.Service{ObjectMeta: metadata(name), Spec: corev1.ServiceSpec{Selector: labels, Ports: servicePorts}},
		statefulSet,
		&policyv1.PodDisruptionBudget{ObjectMeta: metadata(name), Spec: policyv1.PodDisruptionBudgetSpec{MaxUnavailable: &unavailable, Selector: &metav1.LabelSelector{MatchLabels: labels}}},
		network,
	}
	if spec.PodMonitor {
		objects = append(objects, podMonitor(parent))
	}
	return objects
}

func labelsFor(name string) map[string]string {
	return map[string]string{"app.kubernetes.io/name": "nats", "app.kubernetes.io/component": "nats", "app.kubernetes.io/instance": name}
}

func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

type peer struct {
	Current bool   `json:"current"`
	Offline bool   `json:"offline"`
	Lag     uint64 `json:"lag"`
}
type raftGroup struct {
	Leader   string `json:"leader"`
	Replicas []peer `json:"replicas"`
}
type jetstreamReport struct {
	Meta     *raftGroup `json:"meta_cluster"`
	Accounts []struct {
		Name    string `json:"name"`
		Streams []struct {
			Name      string     `json:"name"`
			Cluster   *raftGroup `json:"cluster"`
			Consumers []struct {
				Name    string     `json:"name"`
				Cluster *raftGroup `json:"cluster"`
			} `json:"consumer_detail"`
		} `json:"stream_detail"`
	} `json:"account_details"`
}

func caughtUp(group *raftGroup) bool {
	if group == nil {
		return true
	}
	if group.Leader == "" {
		return false
	}
	for _, replica := range group.Replicas {
		if !replica.Current || replica.Offline || replica.Lag != 0 {
			return false
		}
	}
	return true
}

func fetchJSON(ctx context.Context, url string, target interface{}) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("monitor returned HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(target)
}

func (r *Reconciler) observe(ctx context.Context, object *natsv1.NatsCluster) (bool, string) {
	object.Status.Servers = nil
	object.Status.MetaLeader = ""
	leaders := map[string]string{}
	confirmed := map[string]bool{}
	for ordinal := int32(0); ordinal < object.Spec.Replicas; ordinal++ {
		name := fmt.Sprintf("%s-%d", object.Name, ordinal)
		base := fmt.Sprintf("http://%s.%s-headless.%s.svc:8222", name, object.Name, object.Namespace)
		status := natsv1.ServerStatus{Name: name}
		var health struct {
			Status string `json:"status"`
		}
		if err := fetchJSON(ctx, base+"/healthz", &health); err != nil || health.Status != "ok" {
			object.Status.Servers = append(object.Status.Servers, status)
			return false, "Waiting for JetStream health on " + name
		}
		var report struct {
			Version     string `json:"version"`
			Connections int    `json:"connections"`
			Memory      int64  `json:"mem"`
		}
		if err := fetchJSON(ctx, base+"/varz", &report); err != nil {
			return false, "Waiting for server status on " + name
		}
		status.Version, status.Connections, status.MemoryBytes = report.Version, report.Connections, report.Memory
		var js jetstreamReport
		if err := fetchJSON(ctx, base+"/jsz?accounts=true&streams=true&consumers=true&config=true&raft=true", &js); err != nil {
			return false, "Waiting for JetStream status on " + name
		}
		if object.Spec.Replicas > 1 && js.Meta == nil {
			return false, "Waiting for JetStream metadata membership on " + name
		}
		if !observeGroup(leaders, confirmed, "metadata", name, js.Meta) {
			return false, "JetStream metadata replicas have not caught up on " + name
		}
		if js.Meta != nil {
			object.Status.MetaLeader = js.Meta.Leader
		}
		for _, account := range js.Accounts {
			for _, stream := range account.Streams {
				if !observeGroup(leaders, confirmed, account.Name+"/"+stream.Name, name, stream.Cluster) {
					return false, "A stream replica is unhealthy on " + name
				}
				for _, consumer := range stream.Consumers {
					if !observeGroup(leaders, confirmed, account.Name+"/"+stream.Name+"/"+consumer.Name, name, consumer.Cluster) {
						return false, "A consumer replica is unhealthy on " + name
					}
				}
			}
		}
		status.Healthy = true
		object.Status.Servers = append(object.Status.Servers, status)
	}
	for key := range leaders {
		if !confirmed[key] {
			return false, "Waiting for an authoritative leader report for " + key
		}
	}
	return true, ""
}

func podMonitor(parent *natsv1.NatsCluster) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "monitoring.coreos.com/v1", "kind": "PodMonitor",
		"metadata": map[string]interface{}{"name": parent.Name, "namespace": parent.Namespace},
		"spec": map[string]interface{}{
			"selector":            map[string]interface{}{"matchLabels": map[string]interface{}{"app.kubernetes.io/instance": parent.Name, "app.kubernetes.io/name": "nats", "app.kubernetes.io/component": "nats"}},
			"podMetricsEndpoints": []interface{}{map[string]interface{}{"port": "metrics", "interval": "30s"}},
		},
	}}
}

// Followers do not have authoritative knowledge of other followers' catch-up state.
// Require a healthy report from the leader of every observed Raft group.
func observeGroup(leaders map[string]string, confirmed map[string]bool, key, server string, group *raftGroup) bool {
	if group == nil {
		return true
	}
	if group.Leader == "" {
		return false
	}
	if previous, ok := leaders[key]; ok && previous != group.Leader {
		return false
	}
	leaders[key] = group.Leader
	if server == group.Leader {
		if !caughtUp(group) {
			return false
		}
		confirmed[key] = true
	}
	return true
}
