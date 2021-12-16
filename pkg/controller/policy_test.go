package controller

import (
	"context"
	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	"github.com/loft-sh/jspolicy/pkg/util/testing"
	"github.com/loft-sh/jspolicy/pkg/webhook"
	"gotest.tools/assert"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	testing2 "testing"
	"time"
)

var (
	testPolicy = &policyv1beta1.JsPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test.test.com",
		},
		Spec: policyv1beta1.JsPolicySpec{
			Operations: []admissionregistrationv1.OperationType{"*"},
			Resources:  []string{"pods"},
			ObjectSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"test": "test",
				},
			},
		},
	}
	testPolicy2 = &policyv1beta1.JsPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test2.test.com",
		},
		Spec: policyv1beta1.JsPolicySpec{},
	}
)

type fakeQueue struct {
	arr []interface{}
}

func (f *fakeQueue) Add(item interface{}) {
	f.arr = append(f.arr, item)
}
func (f *fakeQueue) Len() int {
	return len(f.arr)
}
func (f *fakeQueue) Get() (item interface{}, shutdown bool) {
	if len(f.arr) > 0 {
		i := f.arr[0]
		f.arr = f.arr[1:]
		return i, false
	}

	return nil, true
}
func (f *fakeQueue) Done(item interface{})                             {}
func (f *fakeQueue) ShutDown()                                         {}
func (f *fakeQueue) ShuttingDown() bool                                { return false }
func (f *fakeQueue) ShutDownWithDrain()                                {}
func (f *fakeQueue) AddAfter(item interface{}, duration time.Duration) { f.Add(item) }
func (f *fakeQueue) AddRateLimited(item interface{})                   { f.Add(item) }
func (f *fakeQueue) Forget(item interface{})                           {}
func (f *fakeQueue) NumRequeues(item interface{}) int                  { return 0 }

type fakeClient struct {
	client.Client

	nextList *unstructured.UnstructuredList
}

func (f *fakeClient) List(ctx context.Context, obj client.ObjectList, opts ...client.ListOption) error {
	if f.nextList != nil {
		items, err := meta.ExtractList(f.nextList)
		if err != nil {
			return err
		}

		return meta.SetList(obj, items)
	}

	return f.Client.List(ctx, obj, opts...)
}

type fakeHandler struct {
	response    admission.Response
	rawResponse *webhook.Response
}

func (f *fakeHandler) Handle(context.Context, admission.Request, *policyv1beta1.JsPolicy) (admission.Response, *webhook.Response) {
	return f.response, f.rawResponse
}

func TestPolicySimple(t *testing2.T) {
	scheme := testing.NewScheme()
	fakeClient := &fakeClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(testPolicy).Build()}
	fakeQueue := &fakeQueue{arr: []interface{}{}}
	fakeHandler := &fakeHandler{}

	policy := &policyController{
		ctx:           context.Background(),
		policy:        testPolicy.Name,
		managerClient: fakeClient,
		cachedClient:  fakeClient,
		queue:         fakeQueue,
		handler:       fakeHandler,
		scheme:        scheme,
		gvks: []schema.GroupVersionKind{
			{
				Group:   "",
				Kind:    "Pod",
				Version: "v1",
			},
		},
	}

	assert.Equal(t, policy.Name(), testPolicy.Name, "policy name")

	// will not be added
	u := &unstructured.Unstructured{}
	u.SetKind("Pod")
	u.SetName("test")
	u.SetAPIVersion("v1")
	err := policy.Add(u, testPolicy)
	assert.NilError(t, err, "policy add error")
	assert.Equal(t, fakeQueue.Len(), 0, "policy add queue not matching")

	// will be added
	u.SetLabels(map[string]string{"test": "test"})
	err = policy.Add(u, testPolicy)
	assert.NilError(t, err, "policy add error")
	assert.Equal(t, fakeQueue.Len(), 1, "policy add queue matching")

	// set the list the client should return
	fakeClient.nextList = &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*u}}

	// check if requeue works
	err = policy.RequeueAll()
	assert.NilError(t, err, "requeue all")
	assert.Equal(t, fakeQueue.Len(), 2, "policy add queue matching")

	// let's check if a reconcile works correctly
	fakeHandler.rawResponse = &webhook.Response{Reschedule: true}

	requeue, err := policy.process("v1||Pod|test|doesnotexit")
	assert.NilError(t, err, "reconcile policy")
	assert.Equal(t, requeue, true, "requeue")
}
