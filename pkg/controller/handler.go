package controller

import (
	"context"
	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sync"
)

type handler struct {
	managerClient client.Client

	policiesLock sync.RWMutex
	policies     []PolicyController
}

func newHandler(managerClient client.Client) *handler {
	return &handler{
		policies:      []PolicyController{},
		managerClient: managerClient,
	}
}

func (h *handler) NumPolicies() int {
	h.policiesLock.RLock()
	defer h.policiesLock.RUnlock()
	return len(h.policies)
}

func (h *handler) RemovePolicy(policy PolicyController) {
	h.policiesLock.Lock()
	defer h.policiesLock.Unlock()

	policyName := policy.Name()
	newPolicies := []PolicyController{}
	for _, p := range h.policies {
		if p.Name() != policyName {
			newPolicies = append(newPolicies, p)
		}
	}
	h.policies = newPolicies
}

func (h *handler) AddPolicy(policy PolicyController) {
	h.policiesLock.Lock()
	defer h.policiesLock.Unlock()

	h.policies = append(h.policies, policy)
}

func (h *handler) OnAdd(obj interface{}) {
	h.add(obj)
}

func (h *handler) OnUpdate(oldObj, newObj interface{}) {
	h.add(newObj)
}

func (h *handler) OnDelete(obj interface{}) {
	// delta fifo may wrap the object in a cache.DeletedFinalStateUnknown, unwrap it
	if deletedFinalStateUnknown, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = deletedFinalStateUnknown.Obj
	}

	h.add(obj)
}

func (h *handler) add(obj interface{}) {
	h.policiesLock.RLock()
	defer h.policiesLock.RUnlock()

	for _, p := range h.policies {
		jsPolicy := &policyv1beta1.JsPolicy{}
		err := h.managerClient.Get(context.Background(), types.NamespacedName{Name: p.Name()}, jsPolicy)
		if err != nil {
			continue
		}

		err = p.Add(obj.(runtime.Object), jsPolicy)
		if err != nil {
			klog.Infof("Error adding object to policy %s: %v", p.Name(), err)
		}
	}
}
