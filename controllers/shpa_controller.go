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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
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
	"math"
	"sidecar-hpa/algorithm"
	"sidecar-hpa/algorithm/util"
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
)

const (
	defaultSyncPeriod = 15 * time.Second
	subsystem         = "shpa_controller"
)

var (
	Log                                                                = logf.Log.WithName(subsystem)
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
	clientSet, err := kubernetes.NewForConfig(clientConfig)
	if err != nil {
		Log.Error(err, "Error while instantiating the Client configuration.")
		return nil, err
	}

	//init the scaleClient
	cachedDiscovery := discocache.NewMemCacheClient(clientSet.Discovery())
	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(cachedDiscovery)
	restMapper.Reset()
	scaleKindResolver := scale.NewDiscoveryScaleKindResolver(clientSet.Discovery())
	scaleClient, err := scale.NewForConfig(clientConfig, restMapper, dynamic.LegacyAPIPathResolverFunc, scaleKindResolver)
	if err != nil {
		Log.Error(err, "Error while instantiating the scale client")
		return nil, err
	}

	replicaCalc := algorithm.GetAlgorithmFunc("shpa")(clientConfig)
	// TODO MAKE SHPA
	r := &SHPAReconciler{
		client:        mgr.GetClient(),
		scaleClient:   scaleClient,
		restMapper:    restMapper,
		Scheme:        mgr.GetScheme(),
		eventRecorder: mgr.GetEventRecorderFor("shpa_controller"),
		replicaCalc:   replicaCalc,
		syncPeriod:    defaultSyncPeriod,
	}

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
	replicaCalc   util.ReplicaCalculatorItf
}

// Reconcile reads that state of the cluster for a SHPA object and makes changes based on the state read
// and what is in the SHPA.Spec
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
// +kubebuilder:rbac:groups=apps,resources=deployment,verbs=get;list;update;patch
// +kubebuilder:rbac:groups=dbishpa.my.shpa,resources=shpas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dbishpa.my.shpa,resources=shpas/status,verbs=get;update;patch
func (r *SHPAReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	logger := Log.WithValues("Request.Namespace", req.Namespace, "Request.Name", req.Name)
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

	if err := r.reconcileSHPA(logger, instance); err != nil {
		logger.Info("Error during reconcileSHPA", "error", err)
		r.eventRecorder.Event(instance, corev1.EventTypeWarning, "FailedProcessSHPA", err.Error())
		setCondition(instance, autoscalingv2.AbleToScale, corev1.ConditionFalse, "FailedProcessSHPA", "Error happened while processing the SHPA")
		// In case of `reconcileSHPA` error, we need to requeue the Resource in order to retry to process it again
		// we put a delay of 1 second in order to not retry directly and limit the number of retries if it only a transient issue.
		return reconcile.Result{RequeueAfter: time.Second}, nil
	}
	return resPepeat, nil
}

// reconcileSHPA is the core of the controller
func (r *SHPAReconciler) reconcileSHPA(logger logr.Logger, shpa *dbishpav1.SHPA) error {
	defer func() {
		if err1 := recover(); err1 != nil {
			logger.Error(fmt.Errorf("recover error"), "RunTime error in reconcileSHPA", "returnValue", err1)
		}
	}()

	// the following line are here to retrieve the GVK of the target ref
	targetGV, err := schema.ParseGroupVersion(shpa.Spec.ScaleTargetRef.APIVersion)
	if err != nil {
		return fmt.Errorf("invalid API version in scale target reference: %v", err)
	}
	targetGK := schema.GroupKind{
		Group: targetGV.Group,
		Kind:  shpa.Spec.ScaleTargetRef.Kind,
	}
	mappings, err := r.restMapper.RESTMappings(targetGK)
	if err != nil {
		return fmt.Errorf("unable to determine resource for scale target reference: %v", err)
	}

	currentScale, targetGR, err := r.getScaleForResourceMappings(shpa.Namespace, shpa.Spec.ScaleTargetRef.Name, mappings)
	if currentScale == nil && strings.Contains(err.Error(), "not found") {
		return err
	}
	currentReplicas := currentScale.Status.Replicas
	logger.Info("Target deploy", "replicas", currentReplicas)
	shpaStatusOriginal := shpa.Status.DeepCopy()

	reference := fmt.Sprintf("%s/%s/%s", shpa.Spec.ScaleTargetRef.Kind, shpa.Namespace, shpa.Spec.ScaleTargetRef.Name)
	setCondition(shpa, autoscalingv2.AbleToScale, corev1.ConditionTrue, "SucceededGetScale", "the SHPA controller was able to get the target's current scale")
	metricStatuses := shpaStatusOriginal.CurrentMetrics
	if metricStatuses == nil {
		metricStatuses = []autoscalingv2.MetricStatus{}
	}

	rescale, desiredReplicas, rescaleReason, err := r.computeReplicas(logger, currentReplicas, currentScale, shpa, metricStatuses, shpaStatusOriginal, reference)
	if err != nil {
		return nil
	}

	if rescale {
		err = r.rescaleReplicas(logger, rescale, currentScale, shpa, metricStatuses, currentReplicas, desiredReplicas, shpaStatusOriginal, rescaleReason, targetGR)
		if err != nil {
			return nil
		}
	} else {
		r.eventRecorder.Eventf(shpa, corev1.EventTypeNormal, "NotScaling", fmt.Sprintf("Decided not to scale %s to %d (last scale time was %v )", reference, desiredReplicas, shpa.Status.LastScaleTime))
		desiredReplicas = currentReplicas
	}

	setStatus(shpa, currentReplicas, desiredReplicas, metricStatuses, rescale)
	return r.updateStatusIfNeeded(shpaStatusOriginal, shpa)

}

func (r *SHPAReconciler) rescaleReplicas(
	logger logr.Logger,
	rescale bool,
	currentScale *autoscalingv1.Scale,
	shpa *dbishpav1.SHPA,
	metricStatus []autoscalingv2.MetricStatus,
	currentReplicas, desiredReplicas int32,
	shpaStatusOriginal *dbishpav1.SHPAStatus,
	rescaleReason string,
	targetGR schema.GroupResource,
) error {
	setCondition(shpa, autoscalingv2.AbleToScale, corev1.ConditionTrue, "ReadyForScale", "the last scaling time was sufficiently old as to warrant a new scale")
	if shpa.Spec.DryRun {
		logger.Info("DryRun mode: scaling change was inhibited", "currentReplicas", currentReplicas, "desiredReplicas", desiredReplicas)
		setStatus(shpa, currentReplicas, desiredReplicas, metricStatus, rescale)
		return r.updateStatusIfNeeded(shpaStatusOriginal, shpa)
	}
	currentScale.Spec.Replicas = desiredReplicas
	_, err := r.scaleClient.Scales(shpa.Namespace).Update(targetGR, currentScale)
	if err != nil {
		r.eventRecorder.Eventf(shpa, corev1.EventTypeWarning, "FailedRescale", fmt.Sprintf("New size: %d; reason: %s; error: %v", desiredReplicas, rescaleReason, err.Error()))
		setCondition(shpa, autoscalingv2.AbleToScale, corev1.ConditionFalse, "FailedUpdateScale", "the HPA controller was unable to update the target scale: %v", err)
		r.setCurrentReplicasInStatus(shpa, currentReplicas)
		if err := r.updateStatusIfNeeded(shpaStatusOriginal, shpa); err != nil {
			r.eventRecorder.Event(shpa, corev1.EventTypeWarning, "FailedUpdateReplicas", err.Error())
			setCondition(shpa, autoscalingv2.AbleToScale, corev1.ConditionFalse, "FailedUpdateReplicas", "the WPA controller was unable to update the number of replicas: %v", err)
			return err
		}
		return err
	}
	setCondition(shpa, autoscalingv2.AbleToScale, corev1.ConditionTrue, dbishpav1.ConditionReasonSucceededRescale, "the HPA controller was able to update the target scale to %d", desiredReplicas)
	r.eventRecorder.Eventf(shpa, corev1.EventTypeNormal, "SuccessfulRescale", fmt.Sprintf("New size: %d; reason: %s", desiredReplicas, rescaleReason))

	logger.Info("Successful rescale", "currentReplicas", currentReplicas, "desiredReplicas", desiredReplicas, "rescaleReason", rescaleReason)
	return nil
}

// computeReplicas check max and min and set default algorithm
func (r *SHPAReconciler) computeReplicas(
	logger logr.Logger,
	currentReplicas int32,
	currentScale *autoscalingv1.Scale,
	shpa *dbishpav1.SHPA,
	metricStatuses []autoscalingv2.MetricStatus,
	shpaStatusOriginal *dbishpav1.SHPAStatus,
	reference string,
) (bool, int32, string, error) {
	proposeReplicas := int32(0)
	desiredReplicas := int32(0)
	rescaleReason := ""
	now := time.Now()
	var err error
	rescale := true
	switch {
	case currentScale.Spec.Replicas == 0:
		// Autoscaling is disabled for this resource
		desiredReplicas = 0
		rescale = false
		setCondition(shpa, autoscalingv2.ScalingActive, corev1.ConditionFalse, "ScalingDisabled", "scaling is disabled since the replica count of the target is zero")
	case currentReplicas > shpa.Spec.MaxReplicas:
		rescaleReason = "Current number of replicas above Spec.MaxReplicas"
		desiredReplicas = shpa.Spec.MaxReplicas
	case currentReplicas < shpa.Spec.MinReplicas:
		rescaleReason = "Current number of replicas below Spec.MinReplicas"
		desiredReplicas = shpa.Spec.MinReplicas
	case currentReplicas == 0:
		rescaleReason = "Current number of replicas must be greater than 0"
		desiredReplicas = 1
	default:
		var metricTimestamp time.Time
		proposeReplicas, metricStatuses, metricTimestamp, err = r.computeReplicasForMetrics(logger, shpa, currentScale)
		if err != nil {
			if err2 := r.updateStatusIfNeeded(shpaStatusOriginal, shpa); err2 != nil {
				r.eventRecorder.Event(shpa, corev1.EventTypeWarning, "FailedUpdateReplicas", err2.Error())
				setCondition(shpa, autoscalingv2.AbleToScale, corev1.ConditionFalse, "FailedUpdateReplicas", "the WPA controller was unable to update the number of replicas: %v", err)
				logger.Info("The SHPA controller was unable to update the number of replicas", "error", err2)
				return false, desiredReplicas, "", err
			}
			r.eventRecorder.Event(shpa, corev1.EventTypeWarning, "FailedComputeMetricsReplicas", err.Error())
			logger.Info("Failed to compute desired number of replicas based on listed metrics.", "reference", reference, "error", err)
			return false, desiredReplicas, "", err
		}
		if proposeReplicas > desiredReplicas {
			desiredReplicas = proposeReplicas
			now = metricTimestamp
		}
		if desiredReplicas > currentReplicas {
			rescaleReason = fmt.Sprintf(" above target")
		}
		if desiredReplicas < currentReplicas {
			rescaleReason = "All metrics below target"
		}
		desiredReplicas = normalizeDesiredReplicas(logger, shpa, currentReplicas, desiredReplicas)
		logger.Info("Normalized Desired replicas", "desiredReplicas", desiredReplicas)

		rescale = shouldScale(logger, shpa, currentReplicas, desiredReplicas, now)

	}
	return rescale, desiredReplicas, rescaleReason, nil
}

func shouldScale(logger logr.Logger, shpa *dbishpav1.SHPA, currentReplicas, desiredReplicas int32, timestamp time.Time) bool {
	if shpa.Status.LastScaleTime == nil {
		logger.Info("No timestamp for the lastScale event")
		return true
	}

	backoffDown := false
	backoffUp := false
	downscaleForbiddenWindow := time.Duration(shpa.Spec.DownscaleForbiddenWindowSeconds) * time.Second
	downscaleCountdown := shpa.Status.LastScaleTime.Add(downscaleForbiddenWindow).Sub(timestamp).Seconds()

	if downscaleCountdown > 0 {
		setCondition(shpa, autoscalingv2.AbleToScale, corev1.ConditionFalse, "BackoffDownscale", "the time since the previous scale is still within the downscale forbidden window")
		backoffDown = true
		logger.Info("Too early to downscale", "lastScaleTime", shpa.Status.LastScaleTime, "nextDownscaleTimestamp", metav1.Time{Time: shpa.Status.LastScaleTime.Add(downscaleForbiddenWindow)}, "lastMetricsTimestamp", metav1.Time{Time: timestamp})
	}
	upscaleForbiddenWindow := time.Duration(shpa.Spec.UpscaleForbiddenWindowSeconds) * time.Second
	upscaleCountdown := shpa.Status.LastScaleTime.Add(upscaleForbiddenWindow).Sub(timestamp).Seconds()

	// Only upscale if there was no rescaling in the last upscaleForbiddenWindow
	if upscaleCountdown > 0 {
		backoffUp = true
		logger.Info("Too early to upscale", "lastScaleTime", shpa.Status.LastScaleTime, "nextUpscaleTimestamp", metav1.Time{Time: shpa.Status.LastScaleTime.Add(upscaleForbiddenWindow)}, "lastMetricsTimestamp", metav1.Time{Time: timestamp})

		if backoffDown {
			setCondition(shpa, autoscalingv2.AbleToScale, corev1.ConditionFalse, "BackoffBoth", "the time since the previous scale is still within both the downscale and upscale forbidden windows")
		} else {
			setCondition(shpa, autoscalingv2.AbleToScale, corev1.ConditionFalse, "BackoffUpscale", "the time since the previous scale is still within the upscale forbidden window")
		}
	}

	return canScale(logger, backoffUp, backoffDown, currentReplicas, desiredReplicas)
}

// canScale ensures that we only scale under the right conditions.
func canScale(logger logr.Logger, backoffUp, backoffDown bool, currentReplicas, desiredReplicas int32) bool {
	if desiredReplicas == currentReplicas {
		logger.Info("Will not scale: number of replicas has not changed")
		return false
	}
	logger.Info("Cooldown status", "backoffUp", backoffUp, "backoffDown", backoffDown, "desiredReplicas", desiredReplicas, "currentReplicas", currentReplicas)
	return !backoffUp && desiredReplicas > currentReplicas || !backoffDown && desiredReplicas < currentReplicas
}

// setCurrentReplicasInStatus sets the current replica count in the status of the HPA.
func (r *SHPAReconciler) setCurrentReplicasInStatus(shpa *dbishpav1.SHPA, currentReplicas int32) {
	setStatus(shpa, currentReplicas, shpa.Status.DesiredReplicas, shpa.Status.CurrentMetrics, false)
}

// setStatus recreates the status of the given WPA, updating the current and
// desired replicas, as well as the metric statuses
func setStatus(shpa *dbishpav1.SHPA, currentReplicas, desiredReplicas int32, metricStatuses []autoscalingv2.MetricStatus, rescale bool) {
	shpa.Status = dbishpav1.SHPAStatus{
		CurrentReplicas: currentReplicas,
		DesiredReplicas: desiredReplicas,
		CurrentMetrics:  metricStatuses,
		LastScaleTime:   shpa.Status.LastScaleTime,
		Conditions:      shpa.Status.Conditions,
	}

	if rescale {
		now := metav1.NewTime(time.Now())
		shpa.Status.LastScaleTime = &now
	}
}

func (r *SHPAReconciler) computeReplicasForMetrics(
	logger logr.Logger,
	shpa *dbishpav1.SHPA,
	scale *autoscalingv1.Scale,
) (replicas int32, statuses []autoscalingv2.MetricStatus, timestamp time.Time, err error) {
	statuses = make([]autoscalingv2.MetricStatus, len(shpa.Spec.Metrics))
	metric := ""

	for i, metricSpec := range shpa.Spec.Metrics {
		if metricSpec.External == nil && metricSpec.Resource == nil {
			continue
		}
		var replicaCountProposal int32
		var utilizationProposal int64
		var timestampProposal time.Time
		var metricNameProposal string
		switch metricSpec.Type {
		case dbishpav1.ExternalMetricSourceType:
			replicaCalculation, errMetricsServer := r.replicaCalc.GetExternalMetricReplicas(logger, scale, metricSpec, shpa)
			if errMetricsServer != nil {
				return replicaCalculation.ReplicaCount, nil, replicaCalculation.Timestamp, errMetricsServer
			}
			metricNameProposal = fmt.Sprintf("%s{%v}", metricSpec.External.MetricName, metricSpec.External.MetricSelector.MatchLabels)

			replicaCountProposal = replicaCalculation.ReplicaCount
			timestampProposal = replicaCalculation.Timestamp
			utilizationProposal = replicaCalculation.Utilization

			statuses[i] = autoscalingv2.MetricStatus{
				Type: autoscalingv2.ExternalMetricSourceType,
				External: &autoscalingv2.ExternalMetricStatus{
					MetricSelector: metricSpec.External.MetricSelector,
					MetricName:     metricSpec.External.MetricName,
					CurrentValue:   *resource.NewMilliQuantity(utilizationProposal, resource.DecimalSI),
				},
			}
		case dbishpav1.ResourceMetricSourceType:
			replicaCalculation, errMetricsServer := r.replicaCalc.GetResourceReplicas(logger, scale, metricSpec, shpa)
			if errMetricsServer != nil {
				return replicaCalculation.ReplicaCount, nil, replicaCalculation.Timestamp, errMetricsServer
			}

			replicaCountProposal = replicaCalculation.ReplicaCount
			timestampProposal = replicaCalculation.Timestamp
			utilizationProposal = replicaCalculation.Utilization

			replicaCountProposal = replicaCalculation.ReplicaCount
			utilizationProposal = replicaCalculation.Utilization
			timestampProposal = replicaCalculation.Timestamp

			statuses[i] = autoscalingv2.MetricStatus{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricStatus{
					Name:                metricSpec.Resource.Name,
					CurrentAverageValue: *resource.NewMilliQuantity(utilizationProposal, resource.DecimalSI),
				},
			}
		default:
			return 0, nil, time.Time{}, fmt.Errorf("metricSpec.Type:%s not supported", metricSpec.Type)
		}

		// replicas will end up being the max of the replicaCountProposal if there are several metrics
		if replicas == 0 || replicaCountProposal > replicas {
			timestamp = timestampProposal
			replicas = replicaCountProposal
			metric = metricNameProposal
		}

	}
	setCondition(shpa, autoscalingv2.ScalingActive, corev1.ConditionTrue, "ValidMetricFound", "the HPA was able to successfully calculate a replica count from %s", metric)

	return replicas, statuses, timestamp, nil
}

// getScaleForResourceMappings attempts to fetch the scale for the
// resource with the given name and namespace, trying each RESTMapping
// in turn until a working on is found. If none work, the first error
// is returned. It returns both the scale, as well as the group-resource from
// the working mapping
func (r *SHPAReconciler) getScaleForResourceMappings(namespace, name string, mappings []*apimeta.RESTMapping) (*autoscalingv1.Scale, schema.GroupResource, error) {
	var errs []error
	var scale *autoscalingv1.Scale
	var targetGR schema.GroupResource
	for _, mapping := range mappings {
		var err error
		targetGR = mapping.Resource.GroupResource()
		scale, err = r.scaleClient.Scales(namespace).Get(targetGR, name)
		if err == nil {
			break
		}
		errs = append(errs, fmt.Errorf("could not get scale for the GV %s, error: %v", mapping.GroupVersionKind.GroupVersion().String(), err.Error()))

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

// Stolen from upstream

// normalizeDesiredReplicas takes the metrics desired replicas value and normalizes it based on the appropriate conditions (i.e. < maxReplicas, >
// minReplicas, etc...)
func normalizeDesiredReplicas(logger logr.Logger, shpa *dbishpav1.SHPA, currentReplicas int32, prenormalizedDesiredReplicas int32) int32 {
	var minReplicas int32

	minReplicas = shpa.Spec.MinReplicas
	desiredReplicas, condition, reason := convertDesiredReplicasWithRules(logger, shpa, currentReplicas, prenormalizedDesiredReplicas, minReplicas, shpa.Spec.MaxReplicas)

	if desiredReplicas == prenormalizedDesiredReplicas {
		setCondition(shpa, autoscalingv2.ScalingLimited, corev1.ConditionFalse, condition, reason)
	} else {
		setCondition(shpa, autoscalingv2.ScalingLimited, corev1.ConditionTrue, condition, reason)
	}

	return desiredReplicas
}

// convertDesiredReplicas performs the actual normalization, without depending on the `SHPA`
func convertDesiredReplicasWithRules(logger logr.Logger, shpa *dbishpav1.SHPA, currentReplicas, desiredReplicas, wpaMinReplicas, wpaMaxReplicas int32) (int32, string, string) {

	var minimumAllowedReplicas int32
	var maximumAllowedReplicas int32
	var possibleLimitingCondition string
	var possibleLimitingReason string

	scaleDownLimit := calculateScaleDownLimit(shpa, currentReplicas)
	// Compute the maximum and minimum number of replicas we can have
	switch {
	case wpaMinReplicas == 0:
		minimumAllowedReplicas = 1
	case desiredReplicas < scaleDownLimit:
		minimumAllowedReplicas = int32(math.Max(float64(scaleDownLimit), float64(wpaMinReplicas)))
		possibleLimitingCondition = "ScaleDownLimit"
		possibleLimitingReason = "the desired replica count is decreasing faster than the maximum scale rate"
		logger.Info("Downscaling rate higher than limit of `scaleDownLimitFactor`, capping the maximum downscale to 'minimumAllowedReplicas'", "scaleDownLimitFactor", fmt.Sprintf("%d", shpa.Spec.ScaleDownLimitFactor), "wpaMinReplicas", wpaMinReplicas, "minimumAllowedReplicas", minimumAllowedReplicas)
	case desiredReplicas >= scaleDownLimit:
		minimumAllowedReplicas = wpaMinReplicas
		possibleLimitingCondition = "TooFewReplicas"
		possibleLimitingReason = "the desired replica count is below the minimum replica count"
	}

	if desiredReplicas < minimumAllowedReplicas {
		return minimumAllowedReplicas, possibleLimitingCondition, possibleLimitingReason
	}

	scaleUpLimit := calculateScaleUpLimit(shpa, currentReplicas)

	if desiredReplicas > scaleUpLimit {
		maximumAllowedReplicas = int32(math.Min(float64(scaleUpLimit), float64(wpaMaxReplicas)))
		logger.Info("Upscaling rate higher than limit of 'ScaleUpLimitFactor' up to 'maximumAllowedReplicas' replicas. Capping the maximum upscale to %d replicas", "scaleUpLimitFactor", fmt.Sprintf("%d", shpa.Spec.ScaleUpLimitFactor), "wpaMaxReplicas", wpaMaxReplicas, "maximumAllowedReplicas", maximumAllowedReplicas)
		possibleLimitingCondition = "ScaleUpLimit"
		possibleLimitingReason = "the desired replica count is increasing faster than the maximum scale rate"
	} else {
		maximumAllowedReplicas = wpaMaxReplicas
		possibleLimitingCondition = "TooManyReplicas"
		possibleLimitingReason = "the desired replica count is above the maximum replica count"
	}

	// make sure the desiredReplicas is between the allowed boundaries.
	if desiredReplicas > maximumAllowedReplicas {
		logger.Info("Returning replicas, condition and reason", "replicas", maximumAllowedReplicas, "condition", possibleLimitingCondition, possibleLimitingReason)
		return maximumAllowedReplicas, possibleLimitingCondition, possibleLimitingReason
	}

	possibleLimitingCondition = "DesiredWithinRange"
	possibleLimitingReason = "the desired count is within the acceptable range"

	return desiredReplicas, possibleLimitingCondition, possibleLimitingReason
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

// Scaleup limit is used to maximize the upscaling rate.
func calculateScaleUpLimit(shpa *dbishpav1.SHPA, currentReplicas int32) int32 {
	// returns TO how much we can upscale, not BY how much.
	return int32(float64(currentReplicas) + math.Max(1, math.Floor(float64(shpa.Spec.ScaleUpLimitFactor)/100*float64(currentReplicas))))
}

// Scaledown limit is used to maximize the downscaling rate.
func calculateScaleDownLimit(shpa *dbishpav1.SHPA, currentReplicas int32) int32 {
	return int32(float64(currentReplicas) - math.Max(1, math.Floor(float64(shpa.Spec.ScaleDownLimitFactor)/100*float64(currentReplicas))))
}
