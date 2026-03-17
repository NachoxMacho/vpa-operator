package main

import (
	"context"
	"log/slog"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type VpaStatefulSetReconciler struct {
	Client    client.Client
	Scheme    *runtime.Scheme
	VpaClient *versioned.Clientset
}

func (v *VpaStatefulSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	slog.Info("reconciling", slog.String("name", req.Name), slog.String("namespace", req.Namespace), slog.Any("NamespacedName", req.NamespacedName))

	statefulset := &appsv1.StatefulSet{}
	err := v.Client.Get(ctx, req.NamespacedName, statefulset)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{
			RequeueAfter: time.Minute,
		}, err
	}

	// If the object is no longer found, we need to remove the VPA
	if apierrors.IsNotFound(err) {
		err = v.VpaClient.AutoscalingV1().VerticalPodAutoscalers(req.Namespace).Delete(ctx, req.Name, v1.DeleteOptions{})
		if err != nil {
			slog.Error("failed to delete vpa", slog.String("error", err.Error()), slog.String("name", req.Name), slog.String("namespace", req.Namespace))
			return ctrl.Result{
				RequeueAfter: time.Minute,
			}, err
		}
		return ctrl.Result{}, nil
	}

	// Check if VPA already exists

	// vpa := &autoscalingv1.VerticalPodAutoscaler{}
	vpa, err := v.VpaClient.AutoscalingV1().VerticalPodAutoscalers(req.Namespace).Get(ctx, req.Name, v1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		// Error besides objet not found
		slog.Error("failed to get vpa", slog.String("error", err.Error()), slog.String("name", req.Name), slog.String("namespace", req.Namespace))
		return ctrl.Result{
			RequeueAfter: time.Minute,
		}, err
	}

	desiredVPA := getVPA(statefulset)
	if err != nil && apierrors.IsNotFound(err) {
		slog.Info("creating vpa", slog.String("name", req.Name), slog.String("namespace", req.Namespace))
		vpa, err = v.VpaClient.AutoscalingV1().VerticalPodAutoscalers(req.Namespace).Create(ctx, desiredVPA, v1.CreateOptions{
			FieldManager: "vpa-operator",
		})
		if err != nil {
			slog.Error("failed to create vpa", slog.String("error", err.Error()), slog.String("name", req.Name), slog.String("namespace", req.Namespace))
			return ctrl.Result{
				RequeueAfter: time.Minute,
			}, err
		}
	}

	if !isCorrectVPA(vpa, desiredVPA) {
		slog.Info("updating vpa", slog.String("name", req.Name), slog.String("namespace", req.Namespace))
		updateVPA(vpa, desiredVPA)
		_, err = v.VpaClient.AutoscalingV1().VerticalPodAutoscalers(req.Namespace).Update(ctx, vpa, v1.UpdateOptions{
			FieldManager: "vpa-operator",
		})
		if err != nil {
			slog.Error("failed to update existing vpa", slog.String("error", err.Error()), slog.String("name", req.Name), slog.String("namespace", req.Namespace))
			return ctrl.Result{
				RequeueAfter: time.Minute,
			}, err
		}
	}

	return ctrl.Result{}, nil
}
