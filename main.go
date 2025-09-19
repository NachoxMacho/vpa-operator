package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/grafana/pyroscope-go"
	pyroscope_pprof "github.com/grafana/pyroscope-go/http/pprof"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/codes"
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
	pyroscopeAddr := os.Getenv("PYROSCOPE_ADDR")

	pyro, err := pyroscope.Start(pyroscope.Config{
		ApplicationName: "vpa-operator",
		ServerAddress:   pyroscopeAddr,
		ProfileTypes: []pyroscope.ProfileType{
			pyroscope.ProfileCPU,
			pyroscope.ProfileInuseObjects,
			pyroscope.ProfileAllocObjects,
			pyroscope.ProfileInuseSpace,
			pyroscope.ProfileAllocSpace,
			pyroscope.ProfileGoroutines,
			pyroscope.ProfileMutexCount,
			pyroscope.ProfileMutexDuration,
			pyroscope.ProfileBlockCount,
			pyroscope.ProfileBlockDuration,
		},
	})
	if err != nil {
		slog.Warn("failed to connect to profiler", slog.String("error", err.Error()))
	}
	defer func() {
		err := pyro.Stop()
		if err != nil {
			slog.Error("stopped profiling", slog.String("error", err.Error()))
		}
	}()

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/debug/pprof", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		mux.HandleFunc("/debug/pprof/profile", pyroscope_pprof.Profile)

		mux.Handle("/metrics", promhttp.Handler())

		mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
			_, span := StartTrace(context.TODO(), "readyz")
			defer span.End()
			w.WriteHeader(204)
			span.SetStatus(codes.Ok, "completed call")
		})
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			_, span := StartTrace(context.TODO(), "healthz")
			defer span.End()
			w.WriteHeader(204)
			span.SetStatus(codes.Ok, "completed call")
		})
		err := http.ListenAndServe(":8080", mux)
		if err != nil {
			slog.Error("error starting healthcheck server", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

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

	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		slog.Error("failed to build client", slog.String("error", err.Error()))
		os.Exit(1)
	}

	vpaClient, err := versioned.NewForConfig(config)
	if err != nil {
		slog.Error("failed to build vpa client", slog.String("error", err.Error()))
		os.Exit(1)
	}

	for {
		err := createVPAs(vpaClient, k8sClient)
		if err != nil {
			slog.Error("failed creating vpas", slog.String("error", err.Error()))
		}

		time.Sleep(5 * time.Minute)
	}
}

func createVPAs(vpaClient *versioned.Clientset, k8sClient *kubernetes.Clientset) error {
	slog.Info("scanning for changes")

	vpas, err := vpaClient.AutoscalingV1().VerticalPodAutoscalers("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to fetch VerticalPodAutoscalers: %w", err)
	}

	deployments, err := k8sClient.AppsV1().Deployments("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to fetch Deployments: %w", err)
	}

	for _, d := range deployments.Items {
		createVPA(&d, "apps/v1", "Deployment",vpas, vpaClient)
	}

	statefulSets, err := k8sClient.AppsV1().StatefulSets("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to fetch StatefulSets: %w", err)
	}

	for _, s := range statefulSets.Items {
		createVPA(&s, "apps/v1", "StatefulSet", vpas, vpaClient)
	}

	daemonSets, err := k8sClient.AppsV1().DaemonSets("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to fetch DaemonSets: %w", err)
	}

	for _, d := range daemonSets.Items {
		createVPA(&d, "apps/v1", "DaemonSet", vpas, vpaClient)
	}

	for _, v := range vpas.Items {
		if slices.ContainsFunc(deployments.Items, func(d appsv1.Deployment) bool { return d.Name == v.Name && d.Namespace == v.Namespace && v.Spec.TargetRef.Kind == "Deployment" }) {
			continue
		}
		if slices.ContainsFunc(statefulSets.Items, func(s appsv1.StatefulSet) bool { return s.Name == v.Name && s.Namespace == v.Namespace && v.Spec.TargetRef.Kind == "StatefulSet" }) {
			continue
		}
		if slices.ContainsFunc(daemonSets.Items, func(d appsv1.DaemonSet) bool { return d.Name == v.Name && d.Namespace == v.Namespace && v.Spec.TargetRef.Kind == "DaemonSet" }) {
			continue
		}
		if strings.HasPrefix(v.Name, "goldilocks") {
			continue
		}
		err := vpaClient.AutoscalingV1().VerticalPodAutoscalers(v.Namespace).Delete(context.TODO(), v.Name, metav1.DeleteOptions{})
		if err != nil {
			return err
		}
		slog.Info("deleted", slog.String("vpa", v.Name), slog.String("namespace", v.Namespace), slog.String("kind", v.Spec.TargetRef.Kind))
	}

	slog.Info("completed scanning for changes")
	return nil
}

type vpaTarget interface {
	GetName() string
	GetNamespace() string
}

func createVPA[T vpaTarget](target T, apiVersion string, kind string,  vpas *autoscalerv1.VerticalPodAutoscalerList, vpaClient *versioned.Clientset) {
	if slices.ContainsFunc(vpas.Items, func(v autoscalerv1.VerticalPodAutoscaler) bool {
		return target.GetName() == v.Name && target.GetNamespace() == v.Namespace
	}) {
		return
	}
	slog.Info("building VPA", slog.String("name", target.GetName()), slog.String("namespace", target.GetNamespace()))

	off := autoscalerv1.UpdateModeOff
	o := autoscalerv1.VerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      target.GetName(),
			Namespace: target.GetNamespace(),
		},
		Spec: autoscalerv1.VerticalPodAutoscalerSpec{
			TargetRef: &autoscalingv1.CrossVersionObjectReference{
				APIVersion: apiVersion,
				Kind:      	kind,
				Name:       target.GetName(),
			},
			UpdatePolicy: &autoscalerv1.PodUpdatePolicy{
				UpdateMode: &off,
			},
		},
	}
	res, err := vpaClient.AutoscalingV1().VerticalPodAutoscalers(target.GetNamespace()).Create(context.TODO(), &o, metav1.CreateOptions{})
	if err != nil {
		slog.Warn("failed to create vpa", slog.String("error", err.Error()), slog.String("name", target.GetName()), slog.String("namespace", target.GetNamespace()))
	}
	slog.Info("created", slog.String("vpa", res.Name), slog.String("namespace", res.Namespace))
}

