package main

import (
	"slices"

	autoscaling "k8s.io/api/autoscaling/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	autoscalingv1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
)

type KubernetesResource interface {
	GetObjectMeta() v1.Object
	GroupVersionKind() schema.GroupVersionKind
}

func getVPA(resource KubernetesResource) *autoscalingv1.VerticalPodAutoscaler {
	off := autoscalingv1.UpdateModeOff
	metadata := resource.GetObjectMeta()
	gvk := resource.GroupVersionKind()
	return &autoscalingv1.VerticalPodAutoscaler{
		ObjectMeta: v1.ObjectMeta{
			Name:      metadata.GetName(),
			Namespace: metadata.GetNamespace(),
			OwnerReferences: []v1.OwnerReference{
				{
					Name:       metadata.GetName(),
					APIVersion: gvk.GroupVersion().String(),
					Kind:       gvk.Kind,
					UID:        metadata.GetUID(),
				},
			},
		},
		Spec: autoscalingv1.VerticalPodAutoscalerSpec{
			TargetRef: &autoscaling.CrossVersionObjectReference{
				APIVersion: gvk.GroupVersion().String(),
				Kind:       gvk.Kind,
				Name:       metadata.GetName(),
			},
			UpdatePolicy: &autoscalingv1.PodUpdatePolicy{
				UpdateMode: &off,
			},
		},
	}
}

func isCorrectVPA(existing *autoscalingv1.VerticalPodAutoscaler, desired *autoscalingv1.VerticalPodAutoscaler) bool {
	if existing.Spec.TargetRef.Kind != desired.Spec.TargetRef.Kind {
		return false
	}

	if existing.Spec.TargetRef.APIVersion != desired.Spec.TargetRef.APIVersion {
		return false
	}

	if existing.Spec.TargetRef.Name != desired.Spec.TargetRef.Name {
		return false
	}

	if *existing.Spec.UpdatePolicy.UpdateMode != autoscalingv1.UpdateModeOff {
		return false
	}

	// Check for owner references existing in list
	for _, ref := range desired.OwnerReferences {
		if !slices.Contains(existing.OwnerReferences, ref) {
			return false
		}
	}

	return true
}

func updateVPA(existing *autoscalingv1.VerticalPodAutoscaler, desired *autoscalingv1.VerticalPodAutoscaler) {
	existing.Spec.TargetRef = desired.Spec.TargetRef
	existing.Spec.UpdatePolicy = desired.Spec.UpdatePolicy
	for _, ref := range desired.OwnerReferences {
		if slices.Contains(existing.OwnerReferences, ref) {
			continue
		}
		existing.OwnerReferences = append(existing.OwnerReferences, ref)
	}
}
