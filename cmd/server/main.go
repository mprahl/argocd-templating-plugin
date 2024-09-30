package main

import (
	"context"
	"log"
	"net"
	"sync"

	"github.com/mprahl/argocd-templating-plugin/resolver"
	"github.com/mprahl/argocd-templating-plugin/templaterequest"
	"google.golang.org/grpc"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/mprahl/argocd-templating-plugin/controller"
)

var scheme = k8sruntime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}

func main() {
	klog.InitFlags(nil)

	ctrllog.SetLogger(klog.Logger{})

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})

	kubeConfig, err := clientConfig.ClientConfig()
	if err != nil {
		klog.Fatalf("Failed to determine the kubeconfig to use: %v", err)
	}

	client := kubernetes.NewForConfigOrDie(kubeConfig)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lis, err := net.Listen("tcp", "localhost:8080")
	if err != nil {
		klog.Fatalf("failed to listen: %v", err)
	}

	appUpdateChan := make(chan event.TypedGenericEvent[ctrlclient.Object], 1024)

	server, resolversSrc := resolver.NewTemplateResolvers(ctx, kubeConfig, client, appUpdateChan)
	if err != nil {
		log.Fatalf("Failed to instantiate the server: %v", err)
	}

	terminatingCtx := ctrl.SetupSignalHandler()

	grpcServer := grpc.NewServer()
	templaterequest.RegisterTemplateResolverServer(grpcServer, server)

	wg := sync.WaitGroup{}

	klog.Info("Starting the server")
	wg.Add(1)

	go func() {
		err := grpcServer.Serve(lis)
		if err != nil {
			klog.Fatalf("The gRPC server failed to serve: %v", err)
		}

		wg.Done()
	}()

	go func() {
		<-terminatingCtx.Done()
		grpcServer.GracefulStop()
	}()

	options := manager.Options{
		Metrics: metricsserver.Options{
			BindAddress: ":8082",
		},
		Scheme: scheme,
		// TODO: Enable this later
		LeaderElection:   false,
		LeaderElectionID: "argocd-templating-plugin.open-cluster-management.io",
	}

	mgr, err := manager.New(kubeConfig, options)
	if err != nil {
		klog.Fatalf("Unable to start the controller-runtime manager: %v", err)
	}

	appReconciler := controller.ArgoCDAppReconciler{Client: mgr.GetClient()}

	err = appReconciler.SetupWithManager(mgr, resolversSrc)
	if err != nil {
		klog.Fatalf("Unable to setup the controller-runtime manager: %v", err)
	}

	wg.Add(1)

	go func() {
		err := mgr.Start(terminatingCtx)
		if err != nil {
			klog.Fatalf("The controller-runtime manager failed to start: %v", err)
		}

		wg.Done()
	}()

	wg.Wait()
}
