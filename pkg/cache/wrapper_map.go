/*
Copyright 2018 The Kubernetes Authors.

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

package cache

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

// InformersMap create and caches Informers for (runtime.Object, schema.GroupVersionKind) pairs.
// It uses a standard parameter codec constructed based on the given generated Scheme.
type InformersMap struct {
	// we abstract over the details of structured/unstructured/metadata with the specificInformerMaps
	// TODO(directxman12): genericize this over different projections now that we have 3 different maps
	unstructured *specificInformersMap

	// Scheme maps runtime.Objects to GroupVersionKinds
	Scheme *runtime.Scheme
}

// NewInformersMap creates a new InformersMap that can create informers for
// both structured and unstructured objects.
func NewInformersMap(config *rest.Config,
	scheme *runtime.Scheme,
	mapper meta.RESTMapper,
	resync time.Duration,
	namespace string) *InformersMap {

	return &InformersMap{
		unstructured: newUnstructuredInformersMap(config, scheme, mapper, resync, namespace),

		Scheme: scheme,
	}
}

// Start calls Run on each of the informers and sets started to true.  Blocks on the context.
func (m *InformersMap) Start(ctx context.Context) error {
	go m.unstructured.Start(ctx)
	<-ctx.Done()
	return nil
}

// WaitForCacheSync waits until all the caches have been started and synced.
func (m *InformersMap) WaitForCacheSync(ctx context.Context) bool {
	syncedFuncs := append([]cache.InformerSynced(nil), m.unstructured.HasSyncedFuncs()...)
	if !m.unstructured.waitForStarted(ctx) {
		return false
	}
	return cache.WaitForCacheSync(ctx.Done(), syncedFuncs...)
}

// Get will create a new Informer and add it to the map of InformersMap if none exists.  Returns
// the Informer from the map.
func (m *InformersMap) Get(ctx context.Context, gvk schema.GroupVersionKind) (bool, *MapEntry, error) {
	obj := &unstructured.Unstructured{}
	obj.SetKind(gvk.Kind)
	obj.SetAPIVersion(gvk.GroupVersion().String())
	return m.unstructured.Get(ctx, gvk, obj)
}

// Delete will stop an Informer and delete it from the map
func (m *InformersMap) Delete(gvk schema.GroupVersionKind) {
	m.unstructured.Delete(gvk)
}

// newUnstructuredInformersMap creates a new InformersMap for unstructured objects.
func newUnstructuredInformersMap(config *rest.Config, scheme *runtime.Scheme, mapper meta.RESTMapper, resync time.Duration, namespace string) *specificInformersMap {
	return newSpecificInformersMap(config, scheme, mapper, resync, namespace, createUnstructuredListWatch)
}
