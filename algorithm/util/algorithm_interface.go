package util

import (
	"github.com/go-logr/logr"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	v1 "sidecar-hpa/api/v1"
)

// ReplicaCalculatorItf interface for ReplicaCalculator
type ReplicaCalculatorItf interface {
	GetExternalMetricReplicas(logger logr.Logger, target *autoscalingv1.Scale, metric v1.MetricSpec, shpa *v1.SHPA) (replicaCalculation ReplicaCalculation, err error)
	//GetObjectMetricReplicas(logger logr.Logger, target *autoscalingv1.Scale, metric v1.MetricSpec, shpa *v1.SHPA) (replicaCalculation ReplicaCalculation, err error)
	//GetPodMetricReplicas(logger logr.Logger, target *autoscalingv1.Scale, metric v1.MetricSpec, shpa *v1.SHPA) (replicaCalculation ReplicaCalculation, err error)
	//GetContainerMetricReplicas(logger logr.Logger, target *autoscalingv1.Scale, metric v1.MetricSpec, shpa *v1.SHPA) (replicaCalculation ReplicaCalculation, err error)
	GetResourceReplicas(logger logr.Logger, target *autoscalingv1.Scale, metric v1.MetricSpec, shpa *v1.SHPA) (replicaCalculation ReplicaCalculation, err error)
}
