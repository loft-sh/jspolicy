package controller

import (
	"context"
	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	"github.com/loft-sh/jspolicy/pkg/util/testing"
	"gotest.tools/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	testing2 "testing"
)

type fakePolicy struct {
	name  string
	added int
}

func (f *fakePolicy) Add(obj runtime.Object, jsPolicy *policyv1beta1.JsPolicy) error {
	f.added += 1
	return nil
}
func (f *fakePolicy) RequeueAll() error         { return nil }
func (f *fakePolicy) Name() string              { return f.name }
func (f *fakePolicy) Start(ctx context.Context) {}

func TestHandlerSimple(t *testing2.T) {
	scheme := testing.NewScheme()
	handler := &handler{
		policies: []PolicyController{
			&fakePolicy{
				name: testPolicy.Name,
			},
			&fakePolicy{
				name: testPolicy2.Name,
			},
			&fakePolicy{
				name: "does-not-exist",
			},
		},
		managerClient: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(testPolicy, testPolicy2).Build(),
	}

	assert.Equal(t, handler.NumPolicies(), 3)

	// add some events
	handler.OnAdd(&corev1.Pod{})
	handler.OnUpdate(nil, &corev1.Pod{})
	handler.OnDelete(&corev1.Pod{})
	assert.Equal(t, handler.policies[0].(*fakePolicy).added, 3, "policy 1")
	assert.Equal(t, handler.policies[1].(*fakePolicy).added, 3, "policy 2")
	assert.Equal(t, handler.policies[2].(*fakePolicy).added, 0, "policy 3")

	// check if we can remove / add a policy
	handler.AddPolicy(&fakePolicy{name: "does-not-exist-2"})
	assert.Equal(t, handler.NumPolicies(), 4)
	handler.RemovePolicy(&fakePolicy{name: testPolicy2.Name})
	assert.Equal(t, handler.NumPolicies(), 3)
}
