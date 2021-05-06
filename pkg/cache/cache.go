package cache

import (
	"context"
	"fmt"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	kubecache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"strings"
	"sync"
	"time"
)

type Cache interface {
	cache.Cache

	// GarbageCollectInformers deletes unused
	GarbageCollectInformers(exclude map[schema.GroupVersionKind]bool)
}

// Options are the optional arguments for creating a new InformersMap object
type Options struct {
	// Scheme is the scheme to use for mapping objects to GroupVersionKinds
	Scheme *runtime.Scheme

	// Mapper is the RESTMapper to use for mapping GroupVersionKinds to Resources
	Mapper meta.RESTMapper

	// Resync is the base frequency the informers are resynced.
	// Defaults to defaultResyncTime.
	// A 10 percent jitter will be added to the Resync period between informers
	// So that all informers will not send list requests simultaneously.
	Resync *time.Duration

	// Cleanup is the duration after which unused informers should be cleaned up
	Cleanup *time.Duration

	// Namespace restricts the cache's ListWatch to the desired namespace
	// Default watches all namespaces
	Namespace string
}

// New initializes and returns a new Cache.
func New(config *rest.Config, opts Options) (Cache, error) {
	opts, err := defaultOpts(opts)
	if err != nil {
		return nil, err
	}
	im := NewInformersMap(config, opts.Scheme, opts.Mapper, *opts.Resync, opts.Namespace)
	return &informerCache{
		InformersMap: im,

		cleanup:  *opts.Cleanup,
		lastUsed: map[schema.GroupVersionKind]time.Time{},
	}, nil
}

// informerCache is a Kubernetes Object cache populated from InformersMap.  informerCache wraps an InformersMap.
type informerCache struct {
	*InformersMap

	cleanup       time.Duration
	lastUsedMutex sync.Mutex
	lastUsed      map[schema.GroupVersionKind]time.Time
}

func (ip *informerCache) GarbageCollectInformers(exclude map[schema.GroupVersionKind]bool) {
	if exclude == nil {
		exclude = map[schema.GroupVersionKind]bool{}
	}

	ip.lastUsedMutex.Lock()
	defer ip.lastUsedMutex.Unlock()

	now := time.Now()
	for gvk, lastUsed := range ip.lastUsed {
		if exclude[gvk] {
			continue
		}

		if lastUsed.Add(ip.cleanup).Before(now) {
			ip.Delete(gvk)
			delete(ip.lastUsed, gvk)
		}
	}
}

func (ip *informerCache) update(gvk schema.GroupVersionKind) {
	ip.lastUsedMutex.Lock()
	defer ip.lastUsedMutex.Unlock()

	ip.lastUsed[gvk] = time.Now()
}

// Get implements Reader
func (ip *informerCache) Get(ctx context.Context, key client.ObjectKey, out client.Object) error {
	gvk, err := apiutil.GVKForObject(out, ip.Scheme)
	if err != nil {
		return err
	}
	started, c, err := ip.InformersMap.Get(ctx, gvk)
	if err != nil {
		return err
	}
	if !started {
		return &cache.ErrCacheNotStarted{}
	}

	defer ip.update(gvk)
	return c.Reader.Get(ctx, key, out)
}

// List implements Reader
func (ip *informerCache) List(ctx context.Context, out client.ObjectList, opts ...client.ListOption) error {
	gvk, err := ip.objectTypeForListObject(out)
	if err != nil {
		return err
	}
	started, c, err := ip.InformersMap.Get(ctx, gvk)
	if err != nil {
		return err
	}
	if !started {
		return &cache.ErrCacheNotStarted{}
	}

	defer ip.update(gvk)
	return c.Reader.List(ctx, out, opts...)
}

// objectTypeForListObject tries to find the runtime.Object and associated GVK
// for a single object corresponding to the passed-in list type. We need them
// because they are used as cache map key.
func (ip *informerCache) objectTypeForListObject(list client.ObjectList) (schema.GroupVersionKind, error) {
	gvk, err := apiutil.GVKForObject(list, ip.Scheme)
	if err != nil {
		return schema.GroupVersionKind{}, err
	}

	if !strings.HasSuffix(gvk.Kind, "List") {
		return schema.GroupVersionKind{}, fmt.Errorf("non-list type %T (kind %q) passed as output", list, gvk)
	}
	// we need the non-list GVK, so chop off the "List" from the end of the kind
	gvk.Kind = gvk.Kind[:len(gvk.Kind)-4]
	return gvk, nil
}

// GetInformerForKind returns the informer for the GroupVersionKind
func (ip *informerCache) GetInformerForKind(ctx context.Context, gvk schema.GroupVersionKind) (cache.Informer, error) {
	_, i, err := ip.InformersMap.Get(ctx, gvk)
	if err != nil {
		return nil, err
	}
	return i.Informer, err
}

// GetInformer returns the informer for the obj
func (ip *informerCache) GetInformer(ctx context.Context, obj client.Object) (cache.Informer, error) {
	gvk, err := apiutil.GVKForObject(obj, ip.Scheme)
	if err != nil {
		return nil, err
	}

	_, i, err := ip.InformersMap.Get(ctx, gvk)
	if err != nil {
		return nil, err
	}
	return i.Informer, err
}

// NeedLeaderElection implements the LeaderElectionRunnable interface
// to indicate that this can be started without requiring the leader lock
func (ip *informerCache) NeedLeaderElection() bool {
	return false
}

// IndexField adds an indexer to the underlying cache, using extraction function to get
// value(s) from the given field.  This index can then be used by passing a field selector
// to List. For one-to-one compatibility with "normal" field selectors, only return one value.
// The values may be anything.  They will automatically be prefixed with the namespace of the
// given object, if present.  The objects passed are guaranteed to be objects of the correct type.
func (ip *informerCache) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	informer, err := ip.GetInformer(ctx, obj)
	if err != nil {
		return err
	}
	return indexByField(informer, field, extractValue)
}

func indexByField(indexer cache.Informer, field string, extractor client.IndexerFunc) error {
	indexFunc := func(objRaw interface{}) ([]string, error) {
		// TODO(directxman12): check if this is the correct type?
		obj, isObj := objRaw.(client.Object)
		if !isObj {
			return nil, fmt.Errorf("object of type %T is not an Object", objRaw)
		}
		meta, err := meta.Accessor(obj)
		if err != nil {
			return nil, err
		}
		ns := meta.GetNamespace()

		rawVals := extractor(obj)
		var vals []string
		if ns == "" {
			// if we're not doubling the keys for the namespaced case, just re-use what was returned to us
			vals = rawVals
		} else {
			// if we need to add non-namespaced versions too, double the length
			vals = make([]string, len(rawVals)*2)
		}
		for i, rawVal := range rawVals {
			// save a namespaced variant, so that we can ask
			// "what are all the object matching a given index *in a given namespace*"
			vals[i] = KeyToNamespacedKey(ns, rawVal)
			if ns != "" {
				// if we have a namespace, also inject a special index key for listing
				// regardless of the object namespace
				vals[i+len(rawVals)] = KeyToNamespacedKey("", rawVal)
			}
		}

		return vals, nil
	}

	return indexer.AddIndexers(kubecache.Indexers{FieldIndexName(field): indexFunc})
}

func defaultOpts(opts Options) (Options, error) {
	if opts.Scheme == nil {
		return opts, fmt.Errorf("need to define a scheme")
	}
	if opts.Mapper == nil {
		return opts, fmt.Errorf("need to define a rest mapper")
	}
	if opts.Resync == nil {
		return opts, fmt.Errorf("need to define a resync time")
	}
	if opts.Cleanup == nil {
		return opts, fmt.Errorf("need to define a cleanup time")
	}

	return opts, nil
}
