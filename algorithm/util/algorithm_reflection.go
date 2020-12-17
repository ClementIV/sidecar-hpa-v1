package util

import (
	"k8s.io/client-go/rest"
	"sidecar-hpa/algorithm/watermark"
)

var Algorithms = map[string]InitFunc{
	"watermark": watermark.NewWatermark,
}

// InitFunc is used to launch a particular controller.  It may run additional "should I activate checks".
// Any error returned will cause the controller process to `Fatal`
// The bool indicates whether the controller was enabled.
type InitFunc func(clientConfig *rest.Config) ReplicaCalculatorItf

func init() {

}

// TODO: return an algorithm from SHPA.Spec.Algorithm
func GetAlgorithmFunc(algName string) InitFunc {

	return Algorithms[algName]
}
