/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2beta1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discocache "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listerv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/scale"
	"k8s.io/client-go/tools/record"
	kuctrl "k8s.io/kubernetes/pkg/controller"
	"k8s.io/kubernetes/pkg/controller/podautoscaler/metrics"
	resourceclient "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"
	"k8s.io/metrics/pkg/client/external_metrics"
	dbishpav1 "sidecar-hpa/api/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"strings"
	"time"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
)

const (
	defaultSyncPeriod = 15 * time.Second
	subsystem         = "shpa_controller"
)

var (
	log                                                                = logf.Log.WithName(subsystem)
	dryRunCondition autoscalingv2.HorizontalPodAutoscalerConditionType = "DryRun"
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

//newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) (reconcile.Reconciler, error) {
	clientConfig := mgr.GetConfig()
	metricsClient := metrics.NewRESTMetricsClient(
		resourceclient.NewForConfigOrDie(clientConfig),
		nil,
		external_metrics.NewForConfigOrDie(clientConfig),
	)
	var stop chan struct{}
	podLister := initializePodInformer(clientConfig, stop)

	clientSet, err := kubernetes.NewForConfig(clientConfig)
	if err != nil {
		log.Error(err, "Error while instantiating the Client configuration.")
		return nil, err
	}

	//init the scaleClient
	cachedDiscovery := discocache.NewMemCacheClient(clientSet.Discovery())
	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(cachedDiscovery)
	restMapper.Reset()
	scaleKindResolver := scale.NewDiscoveryScaleKindResolver(clientSet.Discovery())
	scaleClient, err := scale.NewForConfig(clientConfig, restMapper, dynamic.LegacyAPIPathResolverFunc, scaleKindResolver)
	if err != nil {
		log.Error(err, "Error while instantiating the scale client")
		return nil, err
	}

	replicaCalc := NewReplicaCalculator(metricsClient, podLister)
	// TODO MAKE SHPA
	r := &ReplicaCalculation{}

	return r, nil

}

// Add creates a new SHPA Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started
func Add(mgr manager.Manager) error {
	r, err := newReconciler(mgr)
	if err != nil {
		return err
	}
	return add(mgr, r)
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("sidecar-controller", mgr, controller.Options{Reconciler: r})

	if err != nil {
		return err
	}

	p := predicate.Funcs{UpdateFunc: updatePredicate}
	// Watch for changes to primary resource SHPA
	return c.Watch(&source.Kind{Type: &dbishpav1.SHPA{}}, &handler.EnqueueRequestForObject{}, p)

}

// When the SHPA is changed (status is changed, edited by the user, etc),
// a new "UpdateEvent" is generated and passed to the "updatePredicate" function.
// If the function returns "true", the event is added to the "Reconcile" queue,
// If the function returns "false",the event is skipped.
func updatePredicate(ev event.UpdateEvent) bool {
	oldObj := ev.ObjectOld.(*dbishpav1.SHPA)
	newObj := ev.ObjectNew.(*dbishpav1.SHPA)
	// Add the shpa object to the queue only if the spec has changed
	// Status change should not lead to requeue.
	hasChanged := !apiequality.Semantic.DeepEqual(newObj.Spec, oldObj.Spec)
	if hasChanged {
		// remove prometheus metrics associated to this SHPA, only metrics associated to metrics
		// since other could not changed.
		//cleanupAssociatedMetrics(oldObject, true)
	}
	return hasChanged
}

// SHPAReconciler reconciles a SHPA object
type SHPAReconciler struct {
	// This client, initialized using mgr.client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client        client.Client
	scaleClient   scale.ScalesGetter
	restMapper    apimeta.RESTMapper
	Log           logr.Logger
	Scheme        *runtime.Scheme
	syncPeriod    time.Duration
	eventRecorder record.EventRecorder
	replicaCalc   ReplicaCalculatorItf
}

// Reconcile reads that state of the cluster for a SHPA object and makes changes based on the state read
// and what is in the SHPA.Spec
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
// +kubebuilder:rbac:groups=apps,resources=deployment,verbs=get;list;update;patch
// +kubebuilder:rbac:groups=dbishpa.my.shpa,resources=shpas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dbishpa.my.shpa,resources=shpas/status,verbs=get;update;patch

func (r *SHPAReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	logger := log.WithValues("Request.Namespace", req.Namespace, "Request.Name", req.Name)
	logger.Info("Reconciling SHPA")

	// resRepeat will be returned if we want to re-run reconcile process
	// NB: we can't return non-nil err, as the "reconcile" msg will be added to the rate-limited queue
	// so that it'll slow down if we have several problems in a row
	resPepeat := reconcile.Result{RequeueAfter: r.syncPeriod}

	// Fetch the SHPA instance
	instance := &dbishpav1.SHPA{}
	err := r.client.Get(context.TODO(), req.NamespacedName, instance)

	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
	}

	var needToReturn bool
	if needToReturn, err = r.handleFinalizer(logger, instance); err != nil || needToReturn {
		return reconcile.Result{}, err
	}

	if !dbishpav1.IsDefaultSHPA(instance) {
		logger.Info("Some configuration options are missing, falling back to the default ones")
		defaultSHPA := dbishpav1.DefaultSHPA(instance)
		if err := r.client.Update(context.TODO(), defaultSHPA); err != nil {
			logger.Info("Failed to set the default values during reconciliation", "error", err)
			return reconcile.Result{}, err
		}

		// default values of the SHPA are set. Return and requeue to show them in the spec.
		return reconcile.Result{Requeue: true}, nil
	}
	if err := dbishpav1.CheckSHPAValidity(instance); err != nil {
		logger.Info("Got an invalid SHPA spec", "Instance", req.NamespacedName.String(), "error", err)
		// If the SHPA spec is incorrect (most likely, in "metrics" section) stop processing it
		// when the spec is updated, the shpa will be re-added to the reconcile queue
		r.eventRecorder.Event(instance, corev1.EventTypeWarning, "FailedSpecCheck", err.Error())
		shpaStatusOriginal := instance.Status.DeepCopy()
		setCondition(instance, autoscalingv2.AbleToScale, corev1.ConditionFalse, "FailedSpecCheck", "Invalid SHPA specification: %s", err)

		if err := r.updateStatusIfNeeded(shpaStatusOriginal, instance); err != nil {
			r.eventRecorder.Event(instance, corev1.EventTypeWarning, "FailedUpdateStatus", err.Error())
			return reconcile.Result{}, err
		}

		// we don't requeue here since the errors was added properly in the SHPA.Status
		// and if the user updates the SHPA.Spec the update event will requeue the resource
		return reconcile.Result{}, nil
	}

	if instance.Spec.DryRun {
		setCondition(instance, dryRunCondition, corev1.ConditionTrue, "DryRun mode enabled", "Scaling changes won't be applied")
	} else {
		setCondition(instance, dryRunCondition, corev1.ConditionFalse, "DryRun mode disabled", "Scaling changes can be applied")
	}

	if er := r.reconcile(logger, instance)

	return ctrl.Result{}, nil
}

// reconcileSHPA is the core of the controller
func (r *SHPAReconciler) reconcileSHPA(logger logr.Logger, shpa *dbishpav1.SHPA) error {
	defer func(){
		if err1 := recover();err1 !=nil{
			logger.Error(fmt.Errorf("recover error"), "RunTime error in reconcileSHPA", "returnValue",err1)
		}
	}()

	// the following line are here to retrieve the GVK of the target ref
	targetGV, err := schema.ParseGroupVersion(shpa.Spec.ScaleTargetRef.APIVersion)
	if err != nil {
		return fmt.Errorf("invalid API version in scale target reference: %v",err)
	}
	targetGK := schema.GroupKind{
		Group: targetGV.Group,
		Kind: shpa.Spec.ScaleTargetRef.Kind,
	}
	mappings, err := r.restMapper.RESTMappings(targetGK)
	if err != nil {
		return fmt.Errorf("unable to determine resource for scale target reference: %v",err)
	}

	currentScale, targetGR,err := r.getScaleForResourceMappings(shpa.Namespace,shpa.Spec.ScaleTargetRef.Name, mappings)
	if currentScale == nil && strings.Contains(err.Error(), "not found") {
		return err
	}
	currentReplicas := currentScale.Status.Replicas
	logger.Info("Target deploy", "replicas", currentReplicas)
	wpaStatusOriginal := shpa.Status.DeepCopy()

	reference := fmt.Sprintf("%s/%s/%s",shpa.Spec.ScaleTargetRef.Kind, shpa.Namespace, shpa.Spec.ScaleTargetRef.Name)
	setCondition(shpa, autoscalingv2.AbleToScale, corev1.ConditionTrue, "SucceededGetScale", "the SHPA controller was able to get the target's current scale")

	proposeReplicas := int32(0)
	desiredReplicas := int32(0)
	rescaleReason := ""
	now := time.Now()

	rescale := true
	switch {
	case currentScale.Spec.Replicas == 0:
		// Autoscaling is disabled for this resource
		desiredReplicas = 0
		rescale = false
		setCondition(shpa, autoscalingv2.ScalingActive, corev1.ConditionFalse, "ScalingDisabled","scaling is disabled since the replica count of the target is zero")
	case currentReplicas > shpa.Spec.MaxReplicas:
		rescaleReason = "Current number of replicas above Spec.MaxReplicas"
		desiredReplicas = shpa.Spec.MaxReplicas
	case currentReplicas < shpa.Spec.MinReplicas:
		rescaleReason = "Current number of replicas below Spec.MinReplicas"
		desiredReplicas = shpa.Spec.MinReplicas
	case currentReplicas ==0:
		rescaleReason = "Current number of replicas must be greater than 0"
		desiredReplicas = 1
	default:
		var metricTimestamp time.Time
		proposeReplicas,me
	}
}
// computeReplicas check max and min and set default algorithm
func (r *SHPAReconciler) computeReplicas(currentReplicas int32,currentScale *autoscalingv1.Scale,shpa *dbishpav1.SHPA){
	proposeReplicas := int32(0)
	metricName := ""
	desiredReplicas := int32(0)
	rescaleReason := ""
	now := time.Now()

	rescale := true
	switch {
	case currentScale.Spec.Replicas == 0:
		// Autoscaling is disabled for this resource
		desiredReplicas = 0
		rescale = false
		setCondition(shpa, autoscalingv2.ScalingActive, corev1.ConditionFalse, "ScalingDisabled","scaling is disabled since the replica count of the target is zero")
	case currentReplicas > shpa.Spec.MaxReplicas:
		rescaleReason = "Current number of replicas above Spec.MaxReplicas"
		desiredReplicas = shpa.Spec.MaxReplicas
	case currentReplicas < shpa.Spec.MinReplicas:
		rescaleReason = "Current number of replicas below Spec.MinReplicas"
		desiredReplicas = shpa.Spec.MinReplicas
	case currentReplicas ==0:
		rescaleReason = "Current number of replicas must be greater than 0"
		desiredReplicas = 1
	default:
		var metricTimestamp time.Time
		proposeReplicas,metricName, metricStatuses,me
	}
}

func (r *SHPAReconciler) computeReplicasForMetrics(
	logger logr.Logger,
	shpa *dbishpav1.SHPA,
	scale *autoscalingv1.Scale,
) (replicas int32,statuses []autoscalingv2.MetricStatus, timestamp time.Time,err error){
	statuses = make([]autoscalingv2.MetricStatus, len(shpa.Spec.Metrics))

	minReplicas := float64(shpa.Spec.MinReplicas)

	for i, metricSpec := range shpa.Spec.Metrics {
		if metricSpec.External == nil && metricSpec.Resource==nil {
			continue
		}


		var timestampProposal time.Time
		//var metricNameProposal string
		switch metricSpec.Type {
		case dbishpav1.ExternalMetricSourceType:
			if metricSpec.External.HighWatermark !=nil && metricSpec.External.LowWatermark != nil{
				//metricNameProposal = fmt.Sprintf("%s{%v}", metricSpec.External.MetricName, metricSpec.External.MetricSelector.MatchLabels)

				replicaCalculation, errMetricsServer := r.replicaCalc.GetExternalMetricReplicas(logger, scale,metricSpec, shpa)

				if errMetricsServer != nil {
					r.eventRecorder.Event(shpa,corev1.EventTypeWarning,"FailedGetExternalMetric", errMetricsServer.Error())
					setCondition(shpa,autoscalingv2.ScalingActive,corev1.ConditionFalse,"FailedGetExternalMetric", "the HPA was unable to compute the replica count: %v",errMetricsServer)
					return 0,nil,time.Time{},fmt.Errorf("failed to get external metric %s: %v", metricSpec.External.MetricName, errMetricsServer)
				}
				timestampProposal = replicaCalculation.timestamp



			}
		}

	}

}
// getScaleForResourceMappings attempts to fetch the scale for the
// resource with the given name and namespace, trying each RESTMapping
// in turn until a working on is found. If none work, the first error
// is returned. It returns both the scale, as well as the group-resource from
// the working mapping
func (r *SHPAReconciler) getScaleForResourceMappings(namespace, name string, mappings []*apimeta.RESTMapping)(*autoscalingv1.Scale,schema.GroupResource,error){
	var errs []error
	var scale * autoscalingv1.Scale
	var targetGR schema.GroupResource
	for _,mapping := range mappings {
		var err error
		targetGR = mapping.Resource.GroupResource()
		scale, err = r.scaleClient.Scales(namespace).Get(targetGR, name)
		if err == nil {
			break
		}
		errs= append(errs,fmt.Errorf("could not get scale for the GV %s, error: %v",mapping.GroupVersionKind.GroupVersion().String(), err.Error()))

	}
	if scale == nil {
		errs = append(errs, fmt.Errorf("scale not found"))
	}
	// make sure we handle an empty set of mappings
	return scale, targetGR, utilerrors.NewAggregate(errs)
}

// updateStatusIfNeeded calls updateStatus only if the status of the new HPA is not same as the old status
func (r *SHPAReconciler) updateStatusIfNeeded(shpaStatus *dbishpav1.SHPAStatus, shpa *dbishpav1.SHPA) error {
	// skip a write if we wouldn't need to update
	if apiequality.Semantic.DeepEqual(shpaStatus, &shpa.Status) {
		return nil
	}
	return r.updateSHPA(shpa)
}

func (r *SHPAReconciler) updateSHPA(shpa *dbishpav1.SHPA) error {
	return r.client.Status().Update(context.TODO(), shpa)
}

// setCondition sets the specific condition type on the given SHPA to the specified value with given reason
// and message. The message and args are treated like a format string. The condition will be added if
// it is not present.
func setCondition(
	shpa *dbishpav1.SHPA,
	conditionType autoscalingv2.HorizontalPodAutoscalerConditionType,
	status corev1.ConditionStatus,
	reason, message string,
	args ...interface{},
) {
	shpa.Status.Conditions = setConditionInList(shpa.Status.Conditions, conditionType, status, reason, message, args...)
}

// setConditionInList sets the specific condition type on the given WPA to the specified value with the given
// reason and message.  The message and args are treated like a format string.  The condition will be added if
// it is not present.  The new list will be returned.
func setConditionInList(inputList []autoscalingv2.HorizontalPodAutoscalerCondition, conditionType autoscalingv2.HorizontalPodAutoscalerConditionType, status corev1.ConditionStatus, reason, message string, args ...interface{}) []autoscalingv2.HorizontalPodAutoscalerCondition {
	resList := inputList
	var existingCond *autoscalingv2.HorizontalPodAutoscalerCondition
	for i, condition := range resList {
		if condition.Type == conditionType {
			// can't take a pointer to an iteration variable
			existingCond = &resList[i]
			break
		}
	}

	if existingCond == nil {
		resList = append(resList, autoscalingv2.HorizontalPodAutoscalerCondition{
			Type: conditionType,
		})
		existingCond = &resList[len(resList)-1]
	}

	if existingCond.Status != status {
		existingCond.LastTransitionTime = metav1.Now()
	}

	existingCond.Status = status
	existingCond.Reason = reason
	existingCond.Message = fmt.Sprintf(message, args...)

	return resList
}

// Stolen from upstream

func (r *SHPAReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbishpav1.SHPA{}).
		Complete(r)
}
