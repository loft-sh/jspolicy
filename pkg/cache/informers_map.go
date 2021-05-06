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
	"fmt"
	"k8s.io/klog"
	"math/rand"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

// clientListWatcherFunc knows how to create a ListWatcher
type createListWatcherFunc func(gvk schema.GroupVersionKind, ip *specificInformersMap) (*cache.ListWatch, error)

// newSpecificInformersMap returns a new specificInformersMap (like
// the generical InformersMap, except that it doesn't implement WaitForCacheSync).
func newSpecificInformersMap(config *rest.Config,
	scheme *runtime.Scheme,
	mapper meta.RESTMapper,
	resync time.Duration,
	namespace string,
	createListWatcher createListWatcherFunc) *specificInformersMap {
	ip := &specificInformersMap{
		config:            config,
		Scheme:            scheme,
		mapper:            mapper,
		informersByGVK:    make(map[schema.GroupVersionKind]*MapEntry),
		codecs:            serializer.NewCodecFactory(scheme),
		paramCodec:        runtime.NewParameterCodec(scheme),
		resync:            resync,
		startWait:         make(chan struct{}),
		createListWatcher: createListWatcher,
		namespace:         namespace,
	}
	return ip
}

// MapEntry contains the cached data for an Informer
type MapEntry struct {
	// StopChan is the channel to stop an informer
	StopChan chan struct{}

	// Informer is the cached informer
	Informer cache.SharedIndexInformer

	// CacheReader wraps Informer and implements the CacheReader interface for a single type
	Reader CacheReader
}

// specificInformersMap create and caches Informers for (runtime.Object, schema.GroupVersionKind) pairs.
// It uses a standard parameter codec constructed based on the given generated Scheme.
type specificInformersMap struct {
	// Scheme maps runtime.Objects to GroupVersionKinds
	Scheme *runtime.Scheme

	// config is used to talk to the apiserver
	config *rest.Config

	// mapper maps GroupVersionKinds to Resources
	mapper meta.RESTMapper

	// informersByGVK is the cache of informers keyed by groupVersionKind
	informersByGVK map[schema.GroupVersionKind]*MapEntry

	// codecs is used to create a new REST client
	codecs serializer.CodecFactory

	// paramCodec is used by list and watch
	paramCodec runtime.ParameterCodec

	// stop is the stop channel to stop informers
	stop <-chan struct{}

	// resync is the base frequency the informers are resynced
	// a 10 percent jitter will be added to the resync period between informers
	// so that all informers will not send list requests simultaneously.
	resync time.Duration

	// mu guards access to the map
	mu sync.RWMutex

	// start is true if the informers have been started
	started bool

	// startWait is a channel that is closed after the
	// informer has been started.
	startWait chan struct{}

	// createClient knows how to create a client and a list object,
	// and allows for abstracting over the particulars of structured vs
	// unstructured objects.
	createListWatcher createListWatcherFunc

	// namespace is the namespace that all ListWatches are restricted to
	// default or empty string means all namespaces
	namespace string
}

// Start calls Run on each of the informers and sets started to true.  Blocks on the context.
// It doesn't return start because it can't return an error, and it's not a runnable directly.
func (ip *specificInformersMap) Start(ctx context.Context) {
	func() {
		ip.mu.Lock()
		defer ip.mu.Unlock()

		// Set the stop channel so it can be passed to informers that are added later
		ip.stop = ctx.Done()

		// Start each informer
		for _, informer := range ip.informersByGVK {
			go informer.Informer.Run(informer.StopChan)
		}

		// Set started to true so we immediately start any informers added later.
		ip.started = true
		close(ip.startWait)
	}()
	<-ctx.Done()

	// make sure to exit all informers
	ip.mu.Lock()
	defer ip.mu.Unlock()

	// Stop each informer
	for key, informer := range ip.informersByGVK {
		close(informer.StopChan)
		delete(ip.informersByGVK, key)
	}
}

func (ip *specificInformersMap) waitForStarted(ctx context.Context) bool {
	select {
	case <-ip.startWait:
		return true
	case <-ctx.Done():
		return false
	}
}

// HasSyncedFuncs returns all the HasSynced functions for the informers in this map.
func (ip *specificInformersMap) HasSyncedFuncs() []cache.InformerSynced {
	ip.mu.RLock()
	defer ip.mu.RUnlock()
	syncedFuncs := make([]cache.InformerSynced, 0, len(ip.informersByGVK))
	for _, informer := range ip.informersByGVK {
		syncedFuncs = append(syncedFuncs, informer.Informer.HasSynced)
	}
	return syncedFuncs
}

// Delete will stop an Informer and delete it from the map
func (ip *specificInformersMap) Delete(gvk schema.GroupVersionKind) {
	ip.mu.Lock()
	defer ip.mu.Unlock()

	i, ok := ip.informersByGVK[gvk]
	if !ok {
		return
	}

	klog.Infof("Stop informer for %s", gvk.String())
	close(i.StopChan)
	delete(ip.informersByGVK, gvk)
}

// Get will create a new Informer and add it to the map of specificInformersMap if none exists.  Returns
// the Informer from the map.
func (ip *specificInformersMap) Get(ctx context.Context, gvk schema.GroupVersionKind, obj runtime.Object) (bool, *MapEntry, error) {
	// Return the informer if it is found
	i, started, ok := func() (*MapEntry, bool, bool) {
		ip.mu.RLock()
		defer ip.mu.RUnlock()
		i, ok := ip.informersByGVK[gvk]
		return i, ip.started, ok
	}()

	if !ok {
		var err error
		if i, started, err = ip.addInformerToMap(gvk, obj); err != nil {
			return started, nil, err
		}
	}

	if started && !i.Informer.HasSynced() {
		// Wait for it to sync before returning the Informer so that folks don't read from a stale cache.
		if !cache.WaitForCacheSync(ctx.Done(), i.Informer.HasSynced) {
			return started, nil, apierrors.NewTimeoutError(fmt.Sprintf("failed waiting for %T Informer to sync", obj), 0)
		}
	}

	return started, i, nil
}

func (ip *specificInformersMap) addInformerToMap(gvk schema.GroupVersionKind, obj runtime.Object) (*MapEntry, bool, error) {
	ip.mu.Lock()
	defer ip.mu.Unlock()

	// Check the cache to see if we already have an Informer.  If we do, return the Informer.
	// This is for the case where 2 routines tried to get the informer when it wasn't in the map
	// so neither returned early, but the first one created it.
	if i, ok := ip.informersByGVK[gvk]; ok {
		return i, ip.started, nil
	}

	// Create a NewSharedIndexInformer and add it to the map.
	klog.Infof("Start informer for %s", gvk.String())
	var lw *cache.ListWatch
	lw, err := ip.createListWatcher(gvk, ip)
	if err != nil {
		return nil, false, err
	}
	ni := cache.NewSharedIndexInformer(lw, obj, resyncPeriod(ip.resync)(), cache.Indexers{
		cache.NamespaceIndex: cache.MetaNamespaceIndexFunc,
	})
	rm, err := ip.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, false, err
	}

	stopChan := make(chan struct{})
	i := &MapEntry{
		Informer: ni,
		Reader:   CacheReader{indexer: ni.GetIndexer(), groupVersionKind: gvk, scopeName: rm.Scope.Name()},
		StopChan: stopChan,
	}
	ip.informersByGVK[gvk] = i

	// Start the Informer if need by
	// TODO(seans): write thorough tests and document what happens here - can you add indexers?
	// can you add eventhandlers?
	if ip.started {
		go i.Informer.Run(stopChan)
	}
	return i, ip.started, nil
}

func createUnstructuredListWatch(gvk schema.GroupVersionKind, ip *specificInformersMap) (*cache.ListWatch, error) {
	// Kubernetes APIs work against Resources, not GroupVersionKinds.  Map the
	// groupVersionKind to the Resource API we will use.
	mapping, err := ip.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, err
	}
	dynamicClient, err := dynamic.NewForConfig(ip.config)
	if err != nil {
		return nil, err
	}

	// TODO: the functions that make use of this ListWatch should be adapted to
	//  pass in their own contexts instead of relying on this fixed one here.
	ctx := context.TODO()
	// Create a new ListWatch for the obj
	return &cache.ListWatch{
		ListFunc: func(opts metav1.ListOptions) (runtime.Object, error) {
			if ip.namespace != "" && mapping.Scope.Name() != meta.RESTScopeNameRoot {
				return dynamicClient.Resource(mapping.Resource).Namespace(ip.namespace).List(ctx, opts)
			}
			return dynamicClient.Resource(mapping.Resource).List(ctx, opts)
		},
		// Setup the watch function
		WatchFunc: func(opts metav1.ListOptions) (watch.Interface, error) {
			// Watch needs to be set to true separately
			opts.Watch = true
			if ip.namespace != "" && mapping.Scope.Name() != meta.RESTScopeNameRoot {
				return dynamicClient.Resource(mapping.Resource).Namespace(ip.namespace).Watch(ctx, opts)
			}
			return dynamicClient.Resource(mapping.Resource).Watch(ctx, opts)
		},
	}, nil
}

// resyncPeriod returns a function which generates a duration each time it is
// invoked; this is so that multiple controllers don't get into lock-step and all
// hammer the apiserver with list requests simultaneously.
func resyncPeriod(resync time.Duration) func() time.Duration {
	return func() time.Duration {
		// the factor will fall into [0.9, 1.1)
		factor := rand.Float64()/5.0 + 0.9
		return time.Duration(float64(resync.Nanoseconds()) * factor)
	}
}
