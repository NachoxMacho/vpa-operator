package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	autoscalerv1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func main() {

	config, err := rest.InClusterConfig()
	if errors.Is(err, rest.ErrNotInCluster) {
		var kubeconfig *string
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
		} else {
			kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
		}
		flag.Parse()
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	}
	if err != nil {
		slog.Error("failed to connect to kubernetes cluster", slog.String("error", err.Error()))
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		slog.Error("failed to build client", slog.String("error", err.Error()))
		os.Exit(1)
	}

	custom, err := versioned.NewForConfig(config)
	if err != nil {
		slog.Error("failed to build vpa client", slog.String("error", err.Error()))
		os.Exit(1)
	}

	for {

		slog.Info("Scanning for changes")

		vpas, err := custom.AutoscalingV1().VerticalPodAutoscalers("").List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			slog.Error("failed to fetch VerticalPodAutoscalers", slog.String("error", err.Error()))
			time.Sleep(5*time.Minute)
			continue
		}

		deployments, err := clientset.AppsV1().Deployments("").List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			slog.Error("failed to fetch Deployments", slog.String("error", err.Error()))
			time.Sleep(5*time.Minute)
			continue
		}

		for _, d := range deployments.Items {
			if slices.ContainsFunc(vpas.Items, func(v autoscalerv1.VerticalPodAutoscaler) bool { return d.Name == v.Name && d.Namespace == v.Namespace }) {
				continue
			}
			slog.Info("Building VPA", slog.String("deployment", d.Name), slog.String("namespace", d.Namespace))

			off := autoscalerv1.UpdateModeOff
			o := autoscalerv1.VerticalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      d.Name,
					Namespace: d.Namespace,
				},
				Spec: autoscalerv1.VerticalPodAutoscalerSpec{
					TargetRef: &autoscalingv1.CrossVersionObjectReference{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       d.Name,
					},
					UpdatePolicy: &autoscalerv1.PodUpdatePolicy{
						UpdateMode: &off,
					},
				},
			}
			res, err := custom.AutoscalingV1().VerticalPodAutoscalers(d.Namespace).Create(context.TODO(), &o, metav1.CreateOptions{})
			if err != nil {
				slog.Error("failed to create vpa", slog.String("error", err.Error()), slog.String("deployment", d.Name), slog.String("namespace", d.Namespace))
			}
			slog.Info("Created", slog.String("vpa", res.Name), slog.String("namespace", res.Namespace))
		}

		for _, v := range vpas.Items {
			if slices.ContainsFunc(deployments.Items, func(d appsv1.Deployment) bool { return d.Name == v.Name && d.Namespace == v.Namespace }) {
				continue
			}
			err := custom.AutoscalingV1().VerticalPodAutoscalers(v.Namespace).Delete(context.TODO(), v.Name, metav1.DeleteOptions{})
			if err != nil {
				log.Fatal(err)
			}
			slog.Info("Deleting", slog.String("vpa", v.Name), slog.String("namespace", v.Namespace))
		}

		time.Sleep(5*time.Minute)
	}
}
