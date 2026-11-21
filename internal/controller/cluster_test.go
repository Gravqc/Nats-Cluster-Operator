package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	natsv1 "example.com/nats-cluster-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func fixture(name string) *natsv1.NatsCluster {
	return &natsv1.NatsCluster{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test", UID: types.UID(name)}, Spec: Spec{Version: "2.15.3", Replicas: 3, CredentialsSecret: "auth", Storage: "1Gi"}}
}

func TestResourcesKeepClusterRoutesStorageAndPlacementIndependent(t *testing.T) {
	a, b := fixture("a"), fixture("b")
	for _, cluster := range []*natsv1.NatsCluster{a, b} {
		resources := Resources(cluster, cluster.Spec)
		config := resources[0].(*corev1.ConfigMap).Data["nats.conf"]
		if !strings.Contains(config, cluster.Name+"-2."+cluster.Name+"-headless.test.svc:6222") {
			t.Fatal("routes missing third peer")
		}
		other := "a"
		if cluster.Name == "a" {
			other = "b"
		}
		if strings.Contains(config, other+"-headless") {
			t.Fatal("cross-cluster route")
		}
		sts := resources[3].(*appsv1.StatefulSet)
		if sts.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType {
			t.Fatal("StatefulSet must not roll independently of health gate")
		}
		if sts.Spec.Template.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].LabelSelector.MatchLabels["app.kubernetes.io/instance"] != cluster.Name {
			t.Fatal("spread must be cluster scoped")
		}
		if sts.Spec.PersistentVolumeClaimRetentionPolicy != nil {
			t.Fatal("default PVC retention must remain Retain")
		}
		if sts.Spec.Template.Spec.Containers[0].Env[2].ValueFrom.SecretKeyRef.Name != "auth" {
			t.Fatal("credentials must come from referenced Secret")
		}
	}
}

func TestReplicaHealthRejectsLagOfflineAndMissingLeader(t *testing.T) {
	cases := []struct {
		group *raftGroup
		want  bool
	}{
		{nil, true}, {&raftGroup{}, false},
		{&raftGroup{Leader: "a", Replicas: []peer{{Current: true}}}, true},
		{&raftGroup{Leader: "a", Replicas: []peer{{Current: true, Lag: 1}}}, false},
		{&raftGroup{Leader: "a", Replicas: []peer{{Current: true, Offline: true}}}, false},
		{&raftGroup{Leader: "a", Replicas: []peer{{Current: false}}}, false},
	}
	for _, test := range cases {
		if caughtUp(test.group) != test.want {
			t.Fatalf("unexpected gate for %+v", test.group)
		}
	}
}

func TestMonitorErrorsBlockActions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }))
	defer server.Close()
	var response map[string]interface{}
	if fetchJSON(context.Background(), server.URL, &response) == nil {
		t.Fatal("HTTP 503 accepted")
	}
}

func TestRetainSurvivesPartialRunAndRejectsForeignResources(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{natsv1.AddToScheme, appsv1.AddToScheme, corev1.AddToScheme, policyv1.AddToScheme, networkingv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	parent := fixture("a")
	parent.Finalizers = []string{finalizer}
	store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(parent).Build()
	reconciler := &Reconciler{Client: store}
	resources := Resources(parent, parent.Spec)
	for _, obj := range resources {
		if err := reconciler.apply(context.Background(), parent, obj); err != nil {
			t.Fatal(err)
		}
	}
	// Running it twice exercises retry after a partially completed finalization.
	for run := 0; run < 2; run++ {
		latest := &natsv1.NatsCluster{}
		if err := store.Get(context.Background(), client.ObjectKeyFromObject(parent), latest); err != nil {
			t.Fatal(err)
		}
		if err := reconciler.retain(context.Background(), latest); err != nil {
			t.Fatal(err)
		}
	}
	for _, obj := range resources {
		if err := store.Get(context.Background(), client.ObjectKeyFromObject(obj), obj); err != nil {
			t.Fatal("retained resource vanished", err)
		}
		if metav1.IsControlledBy(obj, parent) {
			t.Fatal("retained resource still owned by deleted cluster")
		}
	}
	if err := reconciler.apply(context.Background(), parent, resources[0]); err == nil {
		t.Fatal("automatic adoption of retained resource must fail")
	}
}

func TestOnlyRaftLeaderConfirmsFollowerHealth(t *testing.T) {
	leaders := map[string]string{}
	confirmed := map[string]bool{}
	followerView := &raftGroup{Leader: "a", Replicas: []peer{{Current: false}}}
	if !observeGroup(leaders, confirmed, "meta", "b", followerView) || confirmed["meta"] {
		t.Fatal("follower must not confirm another follower's health")
	}
	if observeGroup(leaders, confirmed, "meta", "a", followerView) {
		t.Fatal("leader's unhealthy replica must block")
	}
	if !observeGroup(leaders, confirmed, "meta", "a", &raftGroup{Leader: "a", Replicas: []peer{{Current: true}}}) || !confirmed["meta"] {
		t.Fatal("healthy authoritative report missing")
	}
	if observeGroup(leaders, confirmed, "meta", "c", &raftGroup{Leader: "c"}) {
		t.Fatal("inconsistent leader observations must block")
	}
}

func TestStatusPersistsObservedMonitoringFields(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := natsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cluster := fixture("a")
	store := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&natsv1.NatsCluster{}).WithObjects(cluster).Build()
	current := &natsv1.NatsCluster{}
	key := client.ObjectKeyFromObject(cluster)
	if err := store.Get(context.Background(), key, current); err != nil {
		t.Fatal(err)
	}
	current.Status.Servers = []natsv1.ServerStatus{{Name: "a-0", Version: "2.15.3", Healthy: true}}
	current.Status.MetaLeader = "a-0"
	now := metav1.Now()
	current.Status.HealthySince = &now
	if err := (&Reconciler{Client: store}).status(context.Background(), current, true, "Healthy", "ready"); err != nil {
		t.Fatal(err)
	}
	result := &natsv1.NatsCluster{}
	if err := store.Get(context.Background(), key, result); err != nil {
		t.Fatal(err)
	}
	if len(result.Status.Servers) != 1 || result.Status.MetaLeader != "a-0" || result.Status.HealthySince == nil {
		t.Fatal("observations were not persisted")
	}
}

func TestStandaloneConfigDoesNotWaitForClusterRoutes(t *testing.T) {
	cluster := fixture("local")
	cluster.Spec.Replicas = 1
	objects := Resources(cluster, cluster.Spec)
	if strings.Contains(objects[0].(*corev1.ConfigMap).Data["nats.conf"], "cluster {") {
		t.Fatal("standalone JetStream must not wait for cluster peers")
	}
	if objects[3].(*appsv1.StatefulSet).Spec.PodManagementPolicy != appsv1.ParallelPodManagement {
		t.Fatal("HA startup must not wait for the first pod to form quorum alone")
	}
}
