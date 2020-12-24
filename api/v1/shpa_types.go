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

package v1

import (
	autoscalingv2 "k8s.io/api/autoscaling/v2beta1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// WatermarkPodAutoscaler is the Schema for the watermarkpodautoscalers API
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="value",type="string",JSONPath=".status.currentMetrics[*].external.currentValue.."
// +kubebuilder:printcolumn:name="high shpa",type="string",JSONPath=".spec.metrics[*].external.highWatermark.."
// +kubebuilder:printcolumn:name="low shpa",type="string",JSONPath=".spec.metrics[*].external.lowWatermark.."
// +kubebuilder:printcolumn:name="age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="min replicas",type="integer",JSONPath=".spec.minReplicas"
// +kubebuilder:printcolumn:name="max replicas",type="integer",JSONPath=".spec.maxReplicas"
// +kubebuilder:printcolumn:name="dry-run",type="string",JSONPath=".spec.dryRun"
// +kubebuilder:object:root=true
// SHPA is the Schema for the shpas API
type SHPA struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SHPASpec   `json:"spec,omitempty"`
	Status SHPAStatus `json:"status,omitempty"`
}

// SHPASpec defines the desired state of SHPA
// +k8s:openapi-gen=true
type SHPASpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// part of HorizontalController, see comments in the k8s repo: pkg/controller/podautoscaler/horizontal.go

	// +kubebuilder:validation:Minimum=1
	DownscaleForbiddenWindowSeconds int32 `json:"downscaleForbiddenWindowSeconds,omitempty"`

	// +kubebuilder:validation:Minimum=1
	UpscaleForbiddenWindowSeconds int32 `json:"upscaleForbiddenWindowSeconds,omitempty"`

	// Percentage of replicas that can be added in an upscale event. Max value will set the limit at the Maximum number of Replicas.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	ScaleUpLimitFactor int32 `json:"scaleUpLimitFactor,omitempty"`

	// Percentage of replicas that can be added in an upscale event. Max value will set the limit at the Maximum number of Replicas.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	ScaleDownLimitFactor int32 `json:"scaleDownLimitFactor,omitempty"`

	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:ExclusiveMinimum=true
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:validation:ExclusiveMaximum=true
	Tolerance int32 `json:"tolerance,omitempty"`

	// computed values take the # of replicas into account
	Algorithm string `json:"algorithm,omitempty"`

	// Whether planned scale changes are actually applied
	DryRun bool `json:"dryRun,omitempty"`

	// part of HorizontalPodAutoscalerSpec, see comments in the k8s-1.10.8 repo: staging/src/k8s.io/api/autoscaling/v1/types.go
	// reference to scaled resource; horizontal pod autoscaler will learn the current resource consumption
	// and will set the desired number of pods by using its Scale subresource.
	ScaleTargetRef CrossVersionObjectReference `json:"scaleTargetRef"`
	// specifications that will be used to calculate the desired replica count
	Metrics []MetricSpec `json:"metrics,omitempty"`
	// +kubebuilder:validation:Minimum=1
	MinReplicas int32 `json:"minReplicas,omitempty"`
	// +kubebuilder:validation:Minimum=1
	MaxReplicas int32 `json:"maxReplicas,omitempty"`
	// +kubebuilder:validation:Minimum=1
	ReadinessDelaySeconds int32 `json:"readinessDelay,omitempty"`
}

// SHPAStatus defines the observed state of SHPA
// +k8s:openapi-gen=true
type SHPAStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	ObservedGeneration *int64                                           `json:"observedGeneration,omitempty"`
	LastScaleTime      *metav1.Time                                     `json:"lastScaleTime,omitempty"`
	CurrentReplicas    int32                                            `json:"currentReplicas"`
	DesiredReplicas    int32                                            `json:"desiredReplicas"`
	CurrentMetrics     []autoscalingv2.MetricStatus                     `json:"currentMetrics"`
	Conditions         []autoscalingv2.HorizontalPodAutoscalerCondition `json:"conditions"`
}

// CrossVersionObjectReference contains enough information to let you identify the referred resource.
type CrossVersionObjectReference struct {
	// Kind of the referent; More info: https://git.k8s.io/community/contributors/devel/api-conventions.md#types-kinds"
	Kind string `json:"kind"`
	// Name of the referent; More info: http://kubernetes.io/docs/user-guide/identifiers#names
	Name string `json:"name"`
	// API version of the referent
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// SHPAList contains a list of SHPA
// +k8s:openapi-gen=true
type SHPAList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SHPA `json:"items"`
}

// ExternalMetricSource indicates how to scale on a metric not associated with
// any Kubernetes object (for example length of queue in cloud
// messaging service, or QPS from loadbalancer running outside of cluster).
// Exactly one "target" type should be set.
// +k8s:openapi-gen=true
type ExternalMetricSource struct {
	// metricName is the name of the metric in question.
	MetricName string `json:"metricName"`
	// metricSelector is used to identify a specific time series
	// within a given metric.
	// +optional
	MetricSelector *metav1.LabelSelector `json:"metricSelector,omitempty"`

	HighWatermark *resource.Quantity `json:"highWatermark,omitempty"`
	LowWatermark  *resource.Quantity `json:"lowWatermark,omitempty"`
}

// ResourceMetricSource indicates how to scale on a resource metric known to
// Kubernetes, as specified in requests and limits, describing each pod in the
// current scale target (e.g. CPU or memory).  The values will be averaged
// together before being compared to the target.  Such metrics are built in to
// Kubernetes, and have special scaling options on top of those available to
// normal per-pod metrics using the "pods" source.  Only one "target" type
// should be set.
// +k8s:openapi-gen=true
type ResourceMetricSource struct {
	// name is the name of the resource in question.
	Name v1.ResourceName `json:"name"`
	// metricSelector is used to identify a specific time series
	// within a given metric.
	// +optional
	MetricSelector *metav1.LabelSelector `json:"metricSelector,omitempty"`

	HighWatermark *resource.Quantity `json:"highWatermark,omitempty"`
	LowWatermark  *resource.Quantity `json:"lowWatermark,omitempty"`
}

// MetricSourceType indicates the type of metric.
type MetricSourceType string

var (
	// ExternalMetricSourceType is a global metric that is not associated
	// with any Kubernetes object. It allows autoscaling based on information
	// coming from components running outside of cluster
	// (for example length of queue in cloud messaging service, or
	// QPS from loadbalancer running outside of cluster).
	ExternalMetricSourceType MetricSourceType = "External"

	// ResourceMetricSourceType is a resource metric known to Kubernetes, as
	// specified in requests and limits, describing each pod in the current
	// scale target (e.g. CPU or memory).  Such metrics are built in to
	// Kubernetes, and have special scaling options on top of those available
	// to normal per-pod metrics (the "pods" source).
	ResourceMetricSourceType MetricSourceType = "Resource"
)

// MetricSpec specifies how to scale based on a single metric
// (only `type` and one other matching field should be set at once).
// +k8s:openapi-gen=true
type MetricSpec struct {
	// type is the type of metric source.  It should be one of "Object",
	// "Pods" or "Resource", each mapping to a matching field in the object.
	Type MetricSourceType `json:"type"`
	// external refers to a global metric that is not associated
	// with any Kubernetes object. It allows autoscaling based on information
	// coming from components running outside of cluster
	// (for example length of queue in cloud messaging service, or
	// QPS from loadbalancer running outside of cluster).
	// +optional
	External *ExternalMetricSource `json:"external,omitempty"`
	// resource refers to a resource metric (such as those specified in
	// requests and limits) known to Kubernetes describing each pod in the
	// current scale target (e.g. CPU or memory). Such metrics are built in to
	// Kubernetes, and have special scaling options on top of those available
	// to normal per-pod metrics using the "pods" source.
	// +optional
	Resource *ResourceMetricSource `json:"resource,omitempty"`
}

func init() {
	SchemeBuilder.Register(&SHPA{}, &SHPAList{})
}
