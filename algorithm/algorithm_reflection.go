package algorithm

import (
	"k8s.io/client-go/rest"
	"sidecar-hpa/algorithm/util"
	"sidecar-hpa/algorithm/watermark"
)

var Algorithms = map[string]AlgFunc{
	"shpa": watermark.NewWatermark,
}

// AlgFunc is used to launch a particular controller.  It may run additional "should I activate checks".
// Any error returned will cause the controller process to `Fatal`
// The bool indicates whether the controller was enabled.
type AlgFunc func(clientConfig *rest.Config) util.ReplicaCalculatorItf

// TODO: return an algorithm from SHPA.Spec.Algorithm
func GetAlgorithmFunc(algName string) AlgFunc {

	return Algorithms[algName]
}
