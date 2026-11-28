package controller

import (
	"cmp"
	"context"
	"regexp"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/version"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var natsVersion = regexp.MustCompile(`^2\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)
var legacyNATSImage = regexp.MustCompile(`^nats:2\.([0-9]+)\.([0-9]+)-alpine$`)

func oversizedLegacyImage(image string) bool {
	parts := legacyNATSImage.FindStringSubmatch(image)
	return len(parts) == 3 && (len(strings.TrimLeft(parts[1], "0")) > 9 || len(strings.TrimLeft(parts[2], "0")) > 9)
}

func neverStartedPullFailure(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodPending || podReady(*pod) || !pod.DeletionTimestamp.IsZero() {
		return false
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == "nats" && status.ContainerID == "" && status.ImageID == "" &&
			status.RestartCount == 0 && status.LastTerminationState.Terminated == nil &&
			status.State.Waiting != nil &&
			(status.State.Waiting.Reason == "ErrImagePull" || status.State.Waiting.Reason == "ImagePullBackOff") {
			return true
		}
	}
	return false
}

// Inspect the StatefulSet's own pods using its selector, including old sets
// whose labels may differ from the resource generator's current labels.
func (r *Reconciler) legacyPullFailure(ctx context.Context, sts *appsv1.StatefulSet, targetImage string) (*corev1.Pod, bool, error) {
	selector, err := metav1.LabelSelectorAsSelector(sts.Spec.Selector)
	if err != nil {
		return nil, false, err
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(sts.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, false, err
	}
	legacy, unsafe := oversizedLegacyImage(sts.Spec.Template.Spec.Containers[0].Image), false
	var failed *corev1.Pod
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !metav1.IsControlledBy(pod, sts) {
			continue
		}
		image := pod.Spec.Containers[0].Image
		if image == targetImage {
			continue
		}
		if oversizedLegacyImage(image) {
			legacy = true
			if neverStartedPullFailure(pod) {
				failed = pod
				continue
			}
		}
		unsafe = true
	}
	return failed, legacy && unsafe, nil
}

// A legacy tag may spell the desired version with zero-padded components.
// Only repair an equivalent tag. Normal upgrade validation stays strict.
func sameLegacyVersion(from, to string) bool {
	old, next := strings.Split(from, "."), strings.Split(to, ".")
	return len(old) == 3 && len(next) == 3 && old[0] == "2" && next[0] == "2" &&
		old[1] != "" && old[2] != "" &&
		cmp.Or(strings.TrimLeft(old[1], "0"), "0") == next[1] &&
		cmp.Or(strings.TrimLeft(old[2], "0"), "0") == next[2]
}

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
