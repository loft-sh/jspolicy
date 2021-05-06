package controllers

import (
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type bundleHandler struct {
	client client.Client
}

func (c *bundleHandler) Create(evt event.CreateEvent, queue workqueue.RateLimitingInterface) {
	c.requeue(evt.Object, queue)
}

func (c *bundleHandler) Update(evt event.UpdateEvent, queue workqueue.RateLimitingInterface) {
	c.requeue(evt.ObjectNew, queue)
}

func (c *bundleHandler) Delete(evt event.DeleteEvent, queue workqueue.RateLimitingInterface) {
	c.requeue(evt.Object, queue)
}

func (c *bundleHandler) requeue(obj client.Object, queue workqueue.RateLimitingInterface) {
	queue.Add(reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: obj.GetName(),
		},
	})
}

func (c *bundleHandler) Generic(event.GenericEvent, workqueue.RateLimitingInterface) {
	// Noop
}
