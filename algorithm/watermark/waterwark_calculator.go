package watermark

import (
	"fmt"
	"github.com/go-logr/logr"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	corelisters "k8s.io/client-go/listers/core/v1"
	metricsclient "k8s.io/kubernetes/pkg/controller/podautoscaler/metrics"
	"math"
	"sidecar-hpa/algorithm/util"
	dbishpav1 "sidecar-hpa/api/v1"
	v1 "sidecar-hpa/api/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"strings"
	"time"
)

var log = logf.Log.WithName("watermark_algorithm")

type WatermarkCal struct {
	metricsClient metricsclient.MetricsClient
	podLister     corelisters.PodLister
}

// NewReplicaCalculator returns a ReplicaCalculator object reference
func NewWatermarkReplicaCalculator(metricsClient metricsclient.MetricsClient, podLister corelisters.PodLister) util.ReplicaCalculatorItf {
	return &WatermarkCal{
		metricsClient: metricsClient,
		podLister:     podLister,
	}

}

var _ util.ReplicaCalculatorItf = &WatermarkCal{}

// GetExternalMetricReplicas calculates the desired replica count based on a
// target metric value (as a milli-value) for the external metric in the given
// namespace, and the current replica count.
func (c *WatermarkCal) GetExternalMetricReplicas(logger logr.Logger, target *autoscalingv1.Scale, metric v1.MetricSpec, shpa *v1.SHPA) (util.ReplicaCalculation, error) {
	lbl, err := labels.Parse(target.Status.Selector)
	if err != nil {
		logger.Error(err, "Could not parse the labels of the target")
	}
	currentReadyReplicas, err := c.getReadyPodsCount(target, lbl, time.Duration(shpa.Spec.ReadinessDelaySeconds)*time.Second)
	if err != nil {
		return util.ReplicaCalculation{}, fmt.Errorf("unable to get the number of ready pods across all namespaces for %v: %s", lbl, err.Error())
	}
	averaged := 1.0
	if shpa.Spec.Algorithm == "average" {
		averaged = float64(currentReadyReplicas)
	}

	metricName := metric.External.MetricName
	selector := metric.External.MetricSelector
	labelSelector, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return util.ReplicaCalculation{}, err
	}

	metrics, timestamp, err := c.metricsClient.GetExternalMetric(metricName, shpa.Namespace, labelSelector)
	if err != nil {
		return util.ReplicaCalculation{0, 0, time.Time{}}, fmt.Errorf("unable to get external metric %s/%s/%+v: %s", shpa.Namespace, metricName, selector, err)
	}
	logger.Info("Metrics from the External Metrics Provider", "metrics", metrics)

	var sum int64
	for _, val := range metrics {
		sum += val
	}

	// if the average algorithm is used, the metrics retrieved has to be divided by the number of available replicas.
	adjustedUsage := float64(sum) / averaged
	replicaCount, utilizationQuantity := getReplicaCount(logger, target.Status.Replicas, currentReadyReplicas, shpa, metricName, adjustedUsage, metric.External.LowWatermark, metric.External.HighWatermark)
	return util.ReplicaCalculation{
		replicaCount,
		utilizationQuantity,
		timestamp,
	}, nil
}

// GetResourceReplicas calculates the desired replica count based on a target resource utilization percentage
// of the given resource for pods matching the given selector in the given namespace, and the current replica count
func (c *WatermarkCal) GetResourceReplicas(logger logr.Logger, target *autoscalingv1.Scale, metric v1.MetricSpec, shpa *v1.SHPA) (util.ReplicaCalculation, error) {

	resourceName := metric.Resource.Name
	selector := metric.Resource.MetricSelector
	labelSelector, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return util.ReplicaCalculation{0, 0, time.Time{}}, err
	}

	namespace := shpa.Namespace
	metrics, timestamp, err := c.metricsClient.GetResourceMetric(resourceName, namespace, labelSelector)
	if err != nil {
		return util.ReplicaCalculation{0, 0, time.Time{}}, fmt.Errorf("unable to get resource metric %s/%s/%+v: %s", shpa.Namespace, resourceName, selector, err)
	}
	logger.Info("Metrics from the Resource Client", "metrics", metrics)

	lbl, err := labels.Parse(target.Status.Selector)
	if err != nil {
		return util.ReplicaCalculation{0, 0, time.Time{}}, fmt.Errorf("could not parse the labels of the target: %v", err)
	}

	podList, err := c.podLister.Pods(namespace).List(lbl)
	if err != nil {
		return util.ReplicaCalculation{0, 0, time.Time{}}, fmt.Errorf("unable to get pods while calculating replica count: %v", err)
	}

	if len(podList) == 0 {
		return util.ReplicaCalculation{0, 0, time.Time{}}, fmt.Errorf("no pods returned by selector while calculating replica count")
	}
	readiness := time.Duration(shpa.Spec.ReadinessDelaySeconds) * time.Second
	readyPods, ignoredPods := groupPods(logger, podList, target.Name, metrics, resourceName, readiness)
	readyPodCount := len(readyPods)

	removeMetricsForPods(metrics, ignoredPods)
	if len(metrics) == 0 {
		return util.ReplicaCalculation{0, 0, time.Time{}}, fmt.Errorf("did not receive metrics for any ready pods")
	}

	averaged := 1.0
	if shpa.Spec.Algorithm == "average" {
		averaged = float64(readyPodCount)
	}

	var sum int64
	for _, podMetric := range metrics {
		sum += podMetric.Value
	}
	adjustedUsage := float64(sum) / averaged

	replicaCount, utilizationQuantity := getReplicaCount(logger, target.Status.Replicas, int32(readyPodCount), shpa, string(resourceName), adjustedUsage, metric.Resource.LowWatermark, metric.Resource.HighWatermark)
	return util.ReplicaCalculation{replicaCount, utilizationQuantity, timestamp}, nil
}

func getReplicaCount(logger logr.Logger, currentReplicas, currentReadyReplicas int32, shpa *v1.SHPA, name string, adjustedUsage float64, lowMark, highMark *resource.Quantity) (replicaCount int32, utilization int64) {
	utilizationQuantity := resource.NewMilliQuantity(int64(adjustedUsage), resource.DecimalSI)
	adjustedHM := float64(highMark.MilliValue()) + float64(shpa.Spec.Tolerance)/100*float64(highMark.MilliValue())
	adjustedLM := float64(lowMark.MilliValue()) - float64(shpa.Spec.Tolerance)/100*float64(lowMark.MilliValue())

	switch {
	case adjustedUsage > adjustedHM:
		replicaCount = int32(math.Ceil(float64(currentReadyReplicas) * adjustedUsage / (float64(highMark.MilliValue()))))
		logger.Info("Value is above highMark", "usage", utilizationQuantity.String(), "replicaCount", replicaCount, "currentReadyReplicas", currentReadyReplicas)
	case adjustedUsage < adjustedLM:
		replicaCount = int32(math.Floor(float64(currentReadyReplicas) * adjustedUsage / (float64(lowMark.MilliValue()))))
		// Keep a minimum of 1 replica
		replicaCount = int32(math.Max(float64(replicaCount), 1))
		logger.Info("Value is below lowMark", "usage", utilizationQuantity.String(), "replicaCount", replicaCount, "currentReadyReplicas", currentReadyReplicas)
	default:
		logger.Info("Within bounds of the watermarks", "value", utilizationQuantity.String(), "low watermark", lowMark.String(), "high watermark", highMark.String(), "tolerance", shpa.Spec.Tolerance)
		// returning the currentReplicas instead of the count of healthy ones to be consistent with the upstream behavior.
		return currentReplicas, utilizationQuantity.MilliValue()
	}

	return replicaCount, utilizationQuantity.MilliValue()
}

func (c *WatermarkCal) getReadyPodsCount(target *autoscalingv1.Scale, selector labels.Selector, readinessDelay time.Duration) (int32, error) {
	podList, err := c.podLister.Pods(target.Namespace).List(selector)
	if err != nil {
		return 0, fmt.Errorf("unable to get pods while calculating replica count: %v", err)
	}
	if len(podList) == 0 {
		return 0, fmt.Errorf("no pods returned by selector while calculating replica count")
	}

	toleratedAsReadyPodCount := 0
	var incorrectTargetPodsCount int
	for _, pod := range podList {
		// matchLabel might be too broad, use the OwnerRef to scope over the actual target
		if ok := checkOwnerRef(pod.OwnerReferences, target.Name); !ok {
			incorrectTargetPodsCount++
			continue
		}
		_, condition := getPodCondition(&pod.Status, corev1.PodReady)
		// We can't distinguish pods that are past the Readiness in the lifecycle but have not reached it
		// and pods that are still Unschedulable but we don't need this level of granularity.
		if condition == nil || pod.Status.StartTime == nil {
			log.Info("Pod unready", "namespace", pod.Namespace, "name", pod.Name)
			continue
		}
		if pod.Status.Phase == corev1.PodRunning && condition.Status == corev1.ConditionTrue ||
			// Pending includes the time spent pulling images onto the host.
			// If the pod is stuck in a ContainerCreating state for more than readinessDelay we want to discard it.
			pod.Status.Phase == corev1.PodPending && metav1.Now().Sub((condition.LastTransitionTime).Time) < readinessDelay {
			toleratedAsReadyPodCount++
		}
	}
	log.Info("getReadyPodsCount", "full podList length", len(podList), "toleratedAsReadyPodCount", toleratedAsReadyPodCount, "incorrectly targeted pods", incorrectTargetPodsCount)
	if toleratedAsReadyPodCount == 0 {
		return 0, fmt.Errorf("among the %d pods, none is ready. Skipping recommendation", len(podList))
	}
	return int32(toleratedAsReadyPodCount), nil
}
func checkOwnerRef(ownerRef []metav1.OwnerReference, targetName string) bool {
	for _, o := range ownerRef {
		if o.Kind != "ReplicaSet" && o.Kind != "StatefulSet" {
			continue
		}
		if strings.HasPrefix(o.Name, targetName) {
			return true
		}
	}
	return false
}

func groupPods(logger logr.Logger, podList []*corev1.Pod, targetName string, metrics metricsclient.PodMetricsInfo, resource corev1.ResourceName, delayOfInitialReadinessStatus time.Duration) (readyPods, ignoredPods sets.String) {
	readyPods = sets.NewString()
	ignoredPods = sets.NewString()
	missing := sets.NewString()
	var incorrectTargetPodsCount int
	for _, pod := range podList {
		// matchLabel might be too broad, use the OwnerRef to scope over the actual target
		if ok := checkOwnerRef(pod.OwnerReferences, targetName); !ok {
			incorrectTargetPodsCount++
			continue
		}
		// Failed pods shouldn't produce metrics, but add to ignoredPods to be safe
		if pod.Status.Phase == corev1.PodFailed {
			ignoredPods.Insert(pod.Name)
			continue
		}
		// Pending pods are ignored with Resource metrics.
		if pod.Status.Phase == corev1.PodPending {
			ignoredPods.Insert(pod.Name)
			continue
		}
		// Pods missing metrics
		_, found := metrics[pod.Name]
		if !found {
			missing.Insert(pod.Name)
			continue
		}

		// Unready pods are ignored.
		if resource == corev1.ResourceCPU {
			var ignorePod bool
			_, condition := getPodCondition(&pod.Status, corev1.PodReady)

			if condition == nil || pod.Status.StartTime == nil {
				ignorePod = true
			} else {
				// Ignore metric if pod is unready and it has never been ready.
				ignorePod = condition.Status == corev1.ConditionFalse && pod.Status.StartTime.Add(delayOfInitialReadinessStatus).After(condition.LastTransitionTime.Time)
			}
			if ignorePod {
				ignoredPods.Insert(pod.Name)
				continue
			}
		}
		readyPods.Insert(pod.Name)
	}
	logger.Info("groupPods", "ready", len(readyPods), "missing", len(missing), "ignored", len(ignoredPods), "incorrect target", incorrectTargetPodsCount)
	return readyPods, ignoredPods
}

func removeMetricsForPods(metrics metricsclient.PodMetricsInfo, pods sets.String) {
	for _, pod := range pods.UnsortedList() {
		delete(metrics, pod)
	}
}

// getPodCondition extracts the provided condition from the given status and returns that, and the
// index of the located condition. Returns nil and -1 if the condition is not present.
func getPodCondition(status *corev1.PodStatus, conditionType corev1.PodConditionType) (int, *corev1.PodCondition) {
	if status == nil {
		return -1, nil
	}
	for i := range status.Conditions {
		if status.Conditions[i].Type == conditionType {
			return i, &status.Conditions[i]
		}
	}
	return -1, nil
}

// Scaleup limit is used to maximize the upscaling rate.
func (c *WatermarkCal) CalculateScaleUpLimit(shpa *dbishpav1.SHPA, currentReplicas int32) int32 {
	// returns TO how much we can upscale, not BY how much.
	return int32(float64(currentReplicas) + math.Max(1, math.Floor(float64(shpa.Spec.ScaleUpLimitFactor)/100*float64(currentReplicas))))
}

// Scaledown limit is used to maximize the downscaling rate.
func (c *WatermarkCal) CalculateScaleDownLimit(shpa *dbishpav1.SHPA, currentReplicas int32) int32 {
	return int32(float64(currentReplicas) - math.Max(1, math.Floor(float64(shpa.Spec.ScaleDownLimitFactor)/100*float64(currentReplicas))))
}
