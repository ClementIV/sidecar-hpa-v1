package watermark

import (
	"k8s.io/client-go/informers"
	listerv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	kuctrl "k8s.io/kubernetes/pkg/controller"
	"k8s.io/kubernetes/pkg/controller/podautoscaler/metrics"
	resourceclient "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"
	"k8s.io/metrics/pkg/client/external_metrics"
	"sidecar-hpa/algorithm/util"
	"time"
)

func initializePodInformer(clientConfig *rest.Config, stop chan struct{}) listerv1.PodLister {
	a := kuctrl.SimpleControllerClientBuilder{ClientConfig: clientConfig}
	versionedClient := a.ClientOrDie("sidecar-pod-autoscaler-shared-informer")
	// Only resync every 5 minutes
	// TODO: Consider exposing configuration of the resync for the pod informer.
	sharedInf := informers.NewSharedInformerFactory(versionedClient, 300*time.Second)

	sharedInf.Start(stop)

	go sharedInf.Core().V1().Pods().Informer().Run(stop)

	return sharedInf.Core().V1().Pods().Lister()
}

func NewWatermark(clientConfig *rest.Config) util.ReplicaCalculatorItf {
	metricsClient := metrics.NewRESTMetricsClient(
		resourceclient.NewForConfigOrDie(clientConfig),
		nil,
		external_metrics.NewForConfigOrDie(clientConfig),
	)
	var stop chan struct{}
	podLister := initializePodInformer(clientConfig, stop)

	replicaCalc := NewWatermarkReplicaCalculator(metricsClient, podLister)

	return replicaCalc
}
