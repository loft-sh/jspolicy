package controller

import (
	"context"
	"fmt"
	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	"github.com/loft-sh/jspolicy/pkg/vm/vmpool"
	"github.com/loft-sh/jspolicy/pkg/webhook"
	"github.com/pkg/errors"
	admissionv1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"strings"
	"time"
)

type PolicyController interface {
	Add(obj runtime.Object, jsPolicy *policyv1beta1.JsPolicy) error
	RequeueAll() error
	Name() string
	Start(ctx context.Context)
}

type policyController struct {
	ctx           context.Context
	policy        string
	managerClient client.Client
	cachedClient  client.Reader
	queue         workqueue.RateLimitingInterface
	handler       webhook.Handler
	scheme        *runtime.Scheme
	gvks          []schema.GroupVersionKind
}

func NewPolicyController(managerClient client.Client, cachedClient client.Reader, vmPool vmpool.VMPool, policy string, gvks []schema.GroupVersionKind, scheme *runtime.Scheme) PolicyController {
	return &policyController{
		managerClient: managerClient,
		policy:        policy,
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), policy),
		handler:       webhook.NewHandler(managerClient, vmPool),
		cachedClient:  cachedClient,
		scheme:        scheme,
		gvks:          gvks,
	}
}

func (p *policyController) Name() string {
	return p.policy
}

func keyFunc(obj runtime.Object) (string, error) {
	// key will be Version|Group|Kind|Namespace|Name
	typeAccessor, err := meta.TypeAccessor(obj)
	if err != nil {
		return "", err
	}

	metaAccessor, err := meta.Accessor(obj)
	if err != nil {
		return "", err
	}

	versionGroup, err := schema.ParseGroupVersion(typeAccessor.GetAPIVersion())
	if err != nil {
		return "", err
	}

	return versionGroup.Version + "|" + versionGroup.Group + "|" + typeAccessor.GetKind() + "|" + metaAccessor.GetNamespace() + "|" + metaAccessor.GetName(), nil
}

func (p *policyController) RequeueAll() error {
	jsPolicy := &policyv1beta1.JsPolicy{}
	err := p.managerClient.Get(context.Background(), types.NamespacedName{Name: p.Name()}, jsPolicy)
	if err != nil {
		return nil
	}

	for _, gvk := range p.gvks {
		list := &unstructured.UnstructuredList{}
		list.SetKind(gvk.Kind + "List")
		list.SetAPIVersion(gvk.GroupVersion().String())
		err := p.cachedClient.List(p.ctx, list)
		if err != nil {
			return errors.Wrap(err, "listing objects for "+gvk.String())
		}

		// add all returned objects
		for _, obj := range list.Items {
			obj.SetAPIVersion(gvk.GroupVersion().String())
			obj.SetKind(gvk.Kind)
			err = p.Add(&obj, jsPolicy)
			if err != nil {
				return errors.Wrap(err, "adding object to queue")
			}
		}
	}

	return nil
}

func (p *policyController) Add(obj runtime.Object, jsPolicy *policyv1beta1.JsPolicy) error {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		// looks like an invalid object
		return nil
	}

	// skip objects that do not match the namespace selector
	if accessor.GetNamespace() != "" && jsPolicy.Spec.NamespaceSelector != nil {
		// try to get namespace
		namespaceObj := &corev1.Namespace{}
		err = p.managerClient.Get(context.Background(), types.NamespacedName{Name: accessor.GetNamespace()}, namespaceObj)
		if err != nil {
			// object has a weird namespace and could have been deleted so we skip this
			return nil
		}

		// build selector
		selector, err := metav1.LabelSelectorAsSelector(jsPolicy.Spec.NamespaceSelector)
		if err != nil {
			klog.Warningf("error creating namespace selector for policy %s: %v", p.Name(), err)
			return nil
		}

		// check if labels match
		if selector.Matches(labels.Set(namespaceObj.Labels)) == false {
			return nil
		}
	}

	// skip objects that do not match the object selector
	if jsPolicy.Spec.ObjectSelector != nil {
		// build selector
		selector, err := metav1.LabelSelectorAsSelector(jsPolicy.Spec.ObjectSelector)
		if err != nil {
			klog.Warningf("error creating object selector for policy %s: %v", p.Name(), err)
			return nil
		}

		// check if labels match
		if selector.Matches(labels.Set(accessor.GetLabels())) == false {
			return nil
		}
	}

	key, err := keyFunc(obj)
	if err != nil {
		return err
	}

	p.queue.Add(key)
	return nil
}

// Start is not thread safe and expects the caller to remember if the policy was started already
func (p *policyController) Start(ctx context.Context) {
	defer p.queue.ShutDown()

	p.ctx = ctx
	wait.Until(p.worker(p.queue), time.Second, ctx.Done())
}

func (p *policyController) process(item string) (bool, error) {
	// split the key to find out object information
	splitted := strings.Split(item, "|")
	version, group, kind, namespace, name := splitted[0], splitted[1], splitted[2], splitted[3], splitted[4]

	// construct gvk
	gvk := schema.GroupVersionKind{
		Group:   group,
		Version: version,
		Kind:    kind,
	}

	// get js policy from cache
	jsPolicy := &policyv1beta1.JsPolicy{}
	err := p.managerClient.Get(p.ctx, types.NamespacedName{Name: p.policy}, jsPolicy)
	if err != nil {
		return false, err
	}

	// get operation and object
	operation := admissionv1.Create
	object := &unstructured.Unstructured{}
	object.SetKind(gvk.Kind)
	object.SetAPIVersion(gvk.GroupVersion().String())
	err = p.cachedClient.Get(p.ctx, types.NamespacedName{Namespace: namespace, Name: name}, object)
	if err != nil {
		if kerrors.IsNotFound(err) == false {
			return false, fmt.Errorf("error retrieving object %s/%s from cache: %v", namespace, name, err)
		}

		operation = admissionv1.Delete
		object = nil
	} else if object.GetDeletionTimestamp() != nil {
		operation = admissionv1.Delete
	}

	// check if operation is supported
	found := false
	for _, op := range jsPolicy.Spec.Operations {
		if op == admissionregistrationv1.OperationAll || string(op) == string(operation) {
			found = true
			break
		}
	}
	if !found {
		return false, nil
	}

	// build the admission request
	request := &admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Name:      name,
			Namespace: namespace,
			Operation: operation,
			Kind: metav1.GroupVersionKind{
				Group:   group,
				Version: version,
				Kind:    kind,
			},
		},
	}
	if object != nil {
		request.Object = runtime.RawExtension{
			Object: object,
		}
	}

	// execute the request
	response, rawResponse := p.handler.Handle(p.ctx, *request, jsPolicy)
	for _, w := range response.Warnings {
		klog.Warning(fmt.Sprintf("[%s]: %s", jsPolicy.Name, w))
	}

	// check if we need to log response
	if response.Allowed == false {
		if jsPolicy.Spec.AuditPolicy == nil || *jsPolicy.Spec.AuditPolicy != policyv1beta1.AuditPolicySkip {
			webhook.LogRequest(context.TODO(), p.managerClient, *request, response, jsPolicy, p.scheme, 1)
		}
	}

	// check if we should reschedule
	if rawResponse != nil && rawResponse.Reschedule {
		if rawResponse.Message != "" {
			klog.Info(fmt.Sprintf("[%s]: Reschedule %s because of: %s", jsPolicy.Name, name, rawResponse.Message))
		}

		return true, nil
	}

	return false, nil
}

// worker runs a worker thread that just dequeues items, processes them, and marks them done.
func (p *policyController) worker(queue workqueue.RateLimitingInterface) func() {
	workFunc := func() bool {
		key, quit := queue.Get()
		if quit {
			return true
		}
		defer queue.Done(key)
		reschedule, err := p.process(key.(string))
		if err == nil {
			if reschedule {
				queue.AddRateLimited(key)
				return false
			}

			queue.Forget(key)
			return false
		}
		// for now we ignore errors in the js controller
		klog.Warningf("error in background policy %s: %v", p.policy, err)
		queue.Forget(key)
		// queue.AddRateLimited(key)
		return false
	}

	return func() {
		for {
			if quit := workFunc(); quit {
				return
			}
		}
	}
}
