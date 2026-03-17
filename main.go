package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"

	"github.com/go-logr/logr"
	"github.com/grafana/pyroscope-go"
	pyroscope_pprof "github.com/grafana/pyroscope-go/http/pprof"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/codes"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
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

	vpaClient, err := versioned.NewForConfig(ctrl.GetConfigOrDie())
	if err != nil {
		slog.Error("failed to build vpa client", slog.String("error", err.Error()))
		os.Exit(1)
	}

	log.SetLogger(logr.FromSlogHandler(slog.NewTextHandler(os.Stdout, nil)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), manager.Options{})
	if err != nil {
		slog.Error("failed to create manager", slog.String("error", err.Error()))
		panic(err)
	}

	err = autoscalingv1.AddToScheme(mgr.GetScheme())
	if err != nil {
		slog.Error("failed to add scheme", slog.String("error", err.Error()))
		panic(err)
	}

	err = builder.ControllerManagedBy(mgr).
		For(&appsv1.Deployment{}).
		Owns(&autoscalingv1.VerticalPodAutoscaler{}).
		Watches(&autoscalingv1.VerticalPodAutoscaler{}, handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &appsv1.Deployment{})).
		Complete(&VpaDeploymentReconciler{
			Client:    mgr.GetClient(),
			Scheme:    mgr.GetScheme(),
			VpaClient: vpaClient,
		})
	if err != nil {
		slog.Error("failed to create controller", slog.String("error", err.Error()))
		panic(err)
	}

	err = builder.ControllerManagedBy(mgr).
		For(&appsv1.StatefulSet{}).
		Owns(&autoscalingv1.VerticalPodAutoscaler{}).
		Watches(&autoscalingv1.VerticalPodAutoscaler{}, handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &appsv1.StatefulSet{})).
		Complete(&VpaStatefulSetReconciler{
			Client:    mgr.GetClient(),
			Scheme:    mgr.GetScheme(),
			VpaClient: vpaClient,
		})
	if err != nil {
		slog.Error("failed to create controller", slog.String("error", err.Error()))
		panic(err)
	}

	err = builder.ControllerManagedBy(mgr).
		For(&appsv1.DaemonSet{}).
		Owns(&autoscalingv1.VerticalPodAutoscaler{}).
		Watches(&autoscalingv1.VerticalPodAutoscaler{}, handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &appsv1.DaemonSet{})).
		Complete(&VpaDaemonSetReconciler{
			Client:    mgr.GetClient(),
			Scheme:    mgr.GetScheme(),
			VpaClient: vpaClient,
		})
	if err != nil {
		slog.Error("failed to create controller", slog.String("error", err.Error()))
		panic(err)
	}

	err = mgr.Start(ctrl.SetupSignalHandler())
	if err != nil {
		slog.Error("failed to start manager", slog.String("error", err.Error()))
		panic(err)
	}
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
	err = http.ListenAndServe(":8080", mux)
	if err != nil {
		slog.Error("error starting healthcheck server", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
