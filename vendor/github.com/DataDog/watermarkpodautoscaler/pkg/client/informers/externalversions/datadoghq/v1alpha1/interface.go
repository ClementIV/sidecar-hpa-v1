// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-2019 Datadog, Inc.

// Code generated by informer-gen. DO NOT EDIT.

package v1alpha1

import (
	internalinterfaces "github.com/DataDog/watermarkpodautoscaler/pkg/client/informers/externalversions/internalinterfaces"
)

// Interface provides access to all the informers in this group version.
type Interface interface {
	// WatermarkPodAutoscalers returns a WatermarkPodAutoscalerInformer.
	WatermarkPodAutoscalers() WatermarkPodAutoscalerInformer
}

type version struct {
	factory          internalinterfaces.SharedInformerFactory
	namespace        string
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

// New returns a new Interface.
func New(f internalinterfaces.SharedInformerFactory, namespace string, tweakListOptions internalinterfaces.TweakListOptionsFunc) Interface {
	return &version{factory: f, namespace: namespace, tweakListOptions: tweakListOptions}
}

// WatermarkPodAutoscalers returns a WatermarkPodAutoscalerInformer.
func (v *version) WatermarkPodAutoscalers() WatermarkPodAutoscalerInformer {
	return &watermarkPodAutoscalerInformer{factory: v.factory, namespace: v.namespace, tweakListOptions: v.tweakListOptions}
}
