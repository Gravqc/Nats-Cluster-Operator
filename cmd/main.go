package main

import (
	natsv1 "example.com/nats-cluster-operator/api/v1alpha1"
	"os"

	"example.com/nats-cluster-operator/internal/controller"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	ctrl.SetLogger(zap.New())
	namespace := os.Getenv("WATCH_NAMESPACE")
	if namespace == "" {
		panic("WATCH_NAMESPACE must be set")
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{natsv1.AddToScheme, corev1.AddToScheme, appsv1.AddToScheme, policyv1.AddToScheme, networkingv1.AddToScheme} {
		if err := add(scheme); err != nil {
			panic(err)
		}
	}
	manager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Cache:                   cache.Options{DefaultNamespaces: map[string]cache.Config{namespace: {}}},
		Metrics:                 metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress:  ":8081",
		LeaderElection:          true,
		LeaderElectionID:        "nats-cluster-operator.messaging.nats.example",
		LeaderElectionNamespace: namespace,
	})
	if err != nil {
		panic(err)
	}
	if err = (&controller.Reconciler{Client: manager.GetClient()}).Setup(manager); err != nil {
		panic(err)
	}
	if err = manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		panic(err)
	}
	if err = manager.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		panic(err)
	}
	if err = manager.Start(ctrl.SetupSignalHandler()); err != nil {
		panic(err)
	}
}
