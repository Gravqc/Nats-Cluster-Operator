package controller

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/version"
)

// Leadership is observed anew before each restart. Ordinals break ties only;
// Kubernetes' default reverse-ordinal rollout does not know the NATS leader.
func orderUpgrade(pods []corev1.Pod, metaLeader string) {
	sort.Slice(pods, func(i, j int) bool {
		if pods[i].Name == metaLeader {
			return false
		}
		if pods[j].Name == metaLeader {
			return true
		}
		return pods[i].Name > pods[j].Name
	})
}

func forwardUpgrade(from, to string) bool {
	old, err := version.ParseSemantic(from)
	if err != nil {
		return false
	}
	next, err := version.ParseSemantic(to)
	if err != nil || next.Major() != old.Major() {
		return false
	}
	return (next.Minor() == old.Minor() && next.Patch() >= old.Patch()) || next.Minor() == old.Minor()+1
}
