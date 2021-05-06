package controller

import (
	"context"
	"fmt"
	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	cache2 "github.com/loft-sh/jspolicy/pkg/cache"
	"github.com/loft-sh/jspolicy/pkg/vm/vmpool"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sync"
	"time"
)

type PolicyManager interface {
	Start(ctx context.Context) error
	Update(policy *policyv1beta1.JsPolicy, requeue bool) error
	Delete(name string)
}

type policyManager struct {
	client     client.Client
	restMapper meta.RESTMapper
	vmPool     vmpool.VMPool
	cache      cache2.Cache
	scheme     *runtime.Scheme

	policiesLock sync.Mutex
	policies     map[string]*policy
	// informers is protected by policies lock as well
	informers map[schema.GroupVersionKind]*handler
}

type policy struct {
	cancel     context.CancelFunc
	controller PolicyController
	gvks       []schema.GroupVersionKind
}

func NewControllerPolicyManager(mgr manager.Manager, vmPool vmpool.VMPool, cache cache2.Cache) PolicyManager {
	return &policyManager{
		client:     mgr.GetClient(),
		restMapper: mgr.GetRESTMapper(),
		vmPool:     vmPool,
		cache:      cache,
		scheme:     mgr.GetScheme(),

		policies:  map[string]*policy{},
		informers: map[schema.GroupVersionKind]*handler{},
	}
}

func (p *policyManager) garbageCollectInformers() {
	p.policiesLock.Lock()
	defer p.policiesLock.Unlock()

	// delete the handlers and informers that have no policies
	exclude := map[schema.GroupVersionKind]bool{}
	for gvk, h := range p.informers {
		if h.NumPolicies() == 0 {
			delete(p.informers, gvk)
		} else {
			exclude[gvk] = true
		}
	}

	// cleanup unused informers in the cache
	p.cache.GarbageCollectInformers(exclude)
}

func (p *policyManager) Start(ctx context.Context) error {
	defer p.cleanup()

	// make sure we cleanup unused informers
	go wait.Until(p.garbageCollectInformers, time.Minute, ctx.Done())

	// start the cache
	return p.cache.Start(ctx)
}

func (p *policyManager) cleanup() {
	p.policiesLock.Lock()
	defer p.policiesLock.Unlock()

	// stop policies now
	for k, pol := range p.policies {
		pol.cancel()
		delete(p.policies, k)
	}

	// reset maps
	p.policies = map[string]*policy{}
	p.informers = map[schema.GroupVersionKind]*handler{}

	// garbage collect informers
	p.cache.GarbageCollectInformers(nil)
}

func (p *policyManager) addNoLock(jsPolicy *policyv1beta1.JsPolicy, gvks []schema.GroupVersionKind) error {
	if p.policies[jsPolicy.Name] != nil {
		return nil
	}

	for _, gvk := range gvks {
		if p.informers[gvk] == nil {
			informer, err := p.cache.GetInformerForKind(context.Background(), gvk)
			if err != nil {
				return errors.Wrap(err, "get informer for "+gvk.String())
			}

			handler := newHandler(p.client)
			informer.AddEventHandler(handler)
			p.informers[gvk] = handler
		}
	}

	// create a new policy controller
	controller := NewPolicyController(p.client, p.cache, p.vmPool, jsPolicy.Name, gvks, p.scheme)

	// add policies to handler
	for _, gvk := range gvks {
		p.informers[gvk].AddPolicy(controller)
	}

	// create a stop context for the controller
	controllerContext, cancel := context.WithCancel(context.Background())

	// start the controller
	go controller.Start(controllerContext)

	// add policy to our policies
	p.policies[jsPolicy.Name] = &policy{
		cancel:     cancel,
		controller: controller,
		gvks:       gvks,
	}

	// requeue all objects for policy
	err := controller.RequeueAll()
	if err != nil {
		klog.Warningf("Error re queuing objects for policy %s: %v", jsPolicy.Name, err)
	}

	klog.Infof("Started controller jsPolicy %s", jsPolicy.Name)
	return nil
}

func (p *policyManager) Update(jsPolicy *policyv1beta1.JsPolicy, requeue bool) error {
	// make sure we wait for cache sync here
	p.cache.WaitForCacheSync(context.Background())

	gvks, err := p.gvksFromPolicy(jsPolicy)
	if err != nil {
		return err
	}

	p.policiesLock.Lock()
	defer p.policiesLock.Unlock()

	oldPolicy, ok := p.policies[jsPolicy.Name]
	if !ok {
		return p.addNoLock(jsPolicy, gvks)
	}

	// compare the gvks because thats the only thing that matters
	if p.areGVKsEqual(oldPolicy.gvks, gvks) {
		if requeue {
			return oldPolicy.controller.RequeueAll()
		}

		return nil
	}

	// remove and add policy
	p.deleteNoLock(jsPolicy.Name)
	return p.addNoLock(jsPolicy, gvks)
}

func (p *policyManager) areGVKsEqual(o1 []schema.GroupVersionKind, o2 []schema.GroupVersionKind) bool {
	if len(o1) != len(o2) {
		return false
	}

	for _, gvk := range o1 {
		found := false
		for _, gvk2 := range o2 {
			if gvk.Kind == gvk2.Kind && gvk.Group == gvk2.Group && gvk.Version == gvk2.Version {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}

func (p *policyManager) gvksFromPolicy(policy *policyv1beta1.JsPolicy) ([]schema.GroupVersionKind, error) {
	// first we have to convert the resources to kinds
	if len(policy.Spec.Resources) == 0 {
		return nil, fmt.Errorf("cannot register a policy that has no resources specified")
	}

	allGVKs := map[schema.GroupVersionKind]bool{}
	groupKinds := map[schema.GroupKind]bool{}
	for _, r := range policy.Spec.Resources {
		if r == "*" {
			return nil, fmt.Errorf("wildcard resources are not allowed for background policies")
		}

		gvks, err := p.restMapper.KindsFor(schema.GroupVersionResource{Resource: r})
		if err != nil {
			return nil, errors.Wrap(err, "get kinds for resource "+r)
		}

		for _, gvk := range gvks {
			matchedGroups := false
			if len(policy.Spec.APIGroups) == 0 {
				matchedGroups = true
			} else {
				for _, g := range policy.Spec.APIGroups {
					if g == "*" || g == gvk.Group {
						matchedGroups = true
						break
					}
				}
			}

			matchedVersion := false
			if len(policy.Spec.APIVersions) == 0 {
				matchedVersion = true
			} else {
				for _, g := range policy.Spec.APIVersions {
					if g == "*" || g == gvk.Version {
						matchedVersion = true
						break
					}
				}
			}

			// make sure we take the one with the highest priority from a group
			// and not all versions if multiple match
			groupKind := schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}
			if matchedVersion && matchedGroups && groupKinds[groupKind] == false {
				allGVKs[gvk] = true
				groupKinds[groupKind] = true
			}
		}
	}

	retGVKs := []schema.GroupVersionKind{}
	for k := range allGVKs {
		retGVKs = append(retGVKs, k)
	}
	if len(retGVKs) == 0 {
		return nil, fmt.Errorf("no kinds found for resources %v", policy.Spec.Resources)
	}

	return retGVKs, nil
}

func (p *policyManager) Delete(name string) {
	p.policiesLock.Lock()
	defer p.policiesLock.Unlock()

	p.deleteNoLock(name)
}

func (p *policyManager) deleteNoLock(name string) {
	pol, ok := p.policies[name]
	if !ok {
		return
	}

	klog.Infof("Stop background jsPolicy %s", name)

	// remove policy from all handlers
	for _, h := range p.informers {
		h.RemovePolicy(pol.controller)
	}

	// now stop policy itself
	pol.cancel()
	delete(p.policies, name)
}
