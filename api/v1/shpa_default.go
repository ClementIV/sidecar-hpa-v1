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

import "fmt"

const (
	defaultTolerance                       = 10
	defaultDownscaleForbiddenWindowSeconds = 300
	defaultUpscaleForbiddenWindowSeconds   = 60
	defaultScaleDownLimitFactor            = 20
	defaultScaleUpLimitFactor              = 50
	// Most common use case is to autoscale over avg:kubernetes.cpu.usage, which directly correlates to the # replicas.
	defaultAlgorithm         = "absolute"
	defaultMinReplicas int32 = 1
)

func DefaultSHPA(shpa *SHPA) *SHPA {
	defaultSHPA := shpa.DeepCopy()

	if shpa.Spec.MinReplicas == 0 {
		defaultSHPA.Spec.MinReplicas = defaultMinReplicas
	}
	if shpa.Spec.Algorithm == "" {
		defaultSHPA.Spec.Algorithm = defaultAlgorithm
	}
	if shpa.Spec.Tolerance == 0 {
		defaultSHPA.Spec.Tolerance = defaultTolerance
	}
	if shpa.Spec.ScaleUpLimitFactor == 0 {
		defaultSHPA.Spec.ScaleUpLimitFactor = defaultScaleUpLimitFactor
	}
	if shpa.Spec.ScaleDownLimitFactor == 0 {
		defaultSHPA.Spec.ScaleDownLimitFactor = defaultScaleDownLimitFactor
	}
	if shpa.Spec.DownscaleForbiddenWindowSeconds == 0 {
		defaultSHPA.Spec.DownscaleForbiddenWindowSeconds = defaultDownscaleForbiddenWindowSeconds
	}
	if shpa.Spec.UpscaleForbiddenWindowSeconds == 0 {
		defaultSHPA.Spec.UpscaleForbiddenWindowSeconds = defaultUpscaleForbiddenWindowSeconds
	}

	return defaultSHPA
}

// IsDefaultWatermarkPodAutoscaler used to know if a WatermarkPodAutoscaler has default values
func IsDefaultSHPA(spa *SHPA) bool {

	if spa.Spec.MinReplicas == 0 {
		return false
	}
	if spa.Spec.Algorithm == "" {
		return false
	}
	if spa.Spec.Tolerance == 0 {
		return false
	}
	if spa.Spec.ScaleUpLimitFactor == 0 {
		return false
	}
	if spa.Spec.ScaleDownLimitFactor == 0 {
		return false
	}
	if spa.Spec.DownscaleForbiddenWindowSeconds == 0 {
		return false
	}
	if spa.Spec.UpscaleForbiddenWindowSeconds == 0 {
		return false
	}
	return true
}

// CheckSHPAValidity use to check the validty of a SHPA
// return nil if valid, else an error
func CheckSHPAValidity(shpa *SHPA) error {
	if shpa.Spec.ScaleTargetRef.Kind == "" || shpa.Spec.ScaleTargetRef.Name == "" {
		msg := fmt.Sprintf("the Spec.ScaleTargetRef should be populated, currently Kind:%s and/or Name:%s are not set properly", shpa.Spec.ScaleTargetRef.Kind, shpa.Spec.ScaleTargetRef.Name)
		return fmt.Errorf(msg)
	}
	if shpa.Spec.MinReplicas == 0 || shpa.Spec.MaxReplicas < shpa.Spec.MinReplicas {
		msg := fmt.Sprintf("watermark pod autoscaler requires the minimum number of replicas to be configured and inferior to the maximum")
		return fmt.Errorf(msg)
	}
	return checkSPAMetricsValidity(shpa)
}

func checkSPAMetricsValidity(shpa *SHPA) (err error) {
	// This function will not be needed for the vanilla k8s.
	// For now we check only nil pointers here as they crash the default controller algorithm
	// We also make sure that the Watermarks are properly set.
	for _, metric := range shpa.Spec.Metrics {
		switch metric.Type {
		case "External":
			if metric.External == nil {
				return fmt.Errorf("metric.External is nil while metric.Type is '%s'", metric.Type)
			}
			if metric.External.LowWatermark == nil || metric.External.HighWatermark == nil {
				msg := fmt.Sprintf("Watermarks are not set correctly, removing the WPA %s/%s from the Reconciler", shpa.Namespace, shpa.Name)
				return fmt.Errorf(msg)
			}

			if metric.External.MetricSelector == nil {
				msg := fmt.Sprintf("Missing Labels for the External metric %s", metric.External.MetricName)
				return fmt.Errorf(msg)
			}
			if metric.External.HighWatermark.MilliValue() < metric.External.LowWatermark.MilliValue() {
				msg := fmt.Sprintf("Low WaterMark of External metric %s{%s} has to be strictly inferior to the High Watermark", metric.External.MetricName, metric.External.MetricSelector.MatchLabels)
				return fmt.Errorf(msg)
			}
		case "Resource":
			if metric.Resource == nil {
				return fmt.Errorf("metric.Resource is nil while metric.Type is '%s'", metric.Type)
			}
			if metric.Resource.LowWatermark == nil || metric.Resource.HighWatermark == nil {
				msg := fmt.Sprintf("Watermarks are not set correctly, removing the WPA %s/%s from the Reconciler", shpa.Namespace, shpa.Name)
				return fmt.Errorf(msg)
			}
			if metric.Resource.MetricSelector == nil {
				msg := fmt.Sprintf("Missing Labels for the Resource metric %s", metric.Resource.Name)
				return fmt.Errorf(msg)
			}
			if metric.Resource.HighWatermark.MilliValue() < metric.Resource.LowWatermark.MilliValue() {
				msg := fmt.Sprintf("Low WaterMark of Resource metric %s{%s} has to be strictly inferior to the High Watermark", metric.Resource.Name, metric.Resource.MetricSelector.MatchLabels)
				return fmt.Errorf(msg)
			}
		default:
			return fmt.Errorf("incorrect metric.Type: '%s'", metric.Type)
		}
	}
	return err
}
