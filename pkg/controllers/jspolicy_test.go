package controllers

import (
	"context"
	"errors"
	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	"github.com/loft-sh/jspolicy/pkg/util/conditions"
	"github.com/loft-sh/jspolicy/pkg/util/loghelper"
	"github.com/loft-sh/jspolicy/pkg/util/testing"
	"gotest.tools/assert"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
	testPolicyBundle = &policyv1beta1.JsPolicyBundle{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test.test.com",
		},
		Spec: policyv1beta1.JsPolicyBundleSpec{},
	}

	ifNeeded = admissionregistrationv1.IfNeededReinvocationPolicy

	mutatingTestPolicy = &policyv1beta1.JsPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test.test.com",
		},
		Spec: policyv1beta1.JsPolicySpec{
			Type:       policyv1beta1.PolicyTypeMutating,
			Operations: []admissionregistrationv1.OperationType{"*"},
			Resources:  []string{"pods"},
			ObjectSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"test": "test",
				},
			},
			ReinvocationPolicy: &ifNeeded,
		},
	}
)

func TestSimple(t *testing2.T) {
	err := os.Setenv("KUBE_NAMESPACE", "default")
	assert.NilError(t, err)

	scheme := testing.NewScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(testPolicy).Build()

	controller := &JsPolicyReconciler{
		Client:                  fakeClient,
		Log:                     loghelper.New("test"),
		Scheme:                  scheme,
		Bundler:                 nil,
		ControllerPolicyManager: nil,
		controllerPolicyHash:    map[string]string{},
		CaBundle:                []byte("any"),
	}

	// sync the webhook
	err = controller.syncWebhook(context.Background(), testPolicy)
	assert.NilError(t, err)

	// check if there was a validating webhook created
	list := &admissionregistrationv1.ValidatingWebhookConfigurationList{}
	err = fakeClient.List(context.TODO(), list)
	assert.NilError(t, err)
	assert.Equal(t, len(list.Items), 1)
	var expectedURL *string
	assert.Equal(t, list.Items[0].Webhooks[0].ClientConfig.URL, expectedURL, "the webhook url should be nil when JS_POLICY_WEBHOOK_URL is not set")
	mList := &admissionregistrationv1.MutatingWebhookConfigurationList{}
	err = fakeClient.List(context.TODO(), mList)
	assert.NilError(t, err)
	assert.Equal(t, len(mList.Items), 0)

	// recreate client and check with mutating
	fakeClient = fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(mutatingTestPolicy).Build()
	controller.Client = fakeClient

	// sync the webhook
	err = controller.syncWebhook(context.Background(), mutatingTestPolicy)
	assert.NilError(t, err)

	// check if there was a validating webhook created
	list = &admissionregistrationv1.ValidatingWebhookConfigurationList{}
	err = fakeClient.List(context.TODO(), list)
	assert.NilError(t, err)
	assert.Equal(t, len(list.Items), 0)
	mList = &admissionregistrationv1.MutatingWebhookConfigurationList{}
	err = fakeClient.List(context.TODO(), mList)
	assert.NilError(t, err)
	assert.Equal(t, len(mList.Items), 1)
}
func TestSimpleURL(t *testing2.T) {
	err := os.Setenv("KUBE_NAMESPACE", "default")
	assert.NilError(t, err)
	err = os.Setenv("JS_POLICY_WEBHOOK_URL", "https://testurl.example.local")
	assert.NilError(t, err)

	scheme := testing.NewScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(testPolicy).Build()

	controller := &JsPolicyReconciler{
		Client:                  fakeClient,
		Log:                     loghelper.New("test"),
		Scheme:                  scheme,
		Bundler:                 nil,
		ControllerPolicyManager: nil,
		controllerPolicyHash:    map[string]string{},
		CaBundle:                []byte("any"),
	}

	// sync the webhook
	err = controller.syncWebhook(context.Background(), testPolicy)
	assert.NilError(t, err)

	// check if there was a validating webhook created
	list := &admissionregistrationv1.ValidatingWebhookConfigurationList{}
	err = fakeClient.List(context.TODO(), list)
	assert.NilError(t, err)
	assert.Equal(t, len(list.Items), 1)

	// confirm that the webhook url is set correctly
	expectedURL := "https://testurl.example.local" + "/policy/test.test.com"
	assert.Equal(t, *list.Items[0].Webhooks[0].ClientConfig.URL, expectedURL)
}

type fakeBundler struct {
	bundle []byte
	err    error
}

func (f *fakeBundler) Bundle(javascript string, dependencies map[string]string, timeout time.Duration) ([]byte, error) {
	return f.bundle, f.err
}

func TestCompile(t *testing2.T) {
	scheme := testing.NewScheme()
	fakeBundler := &fakeBundler{}

	controller := &JsPolicyReconciler{
		Client:  fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(testPolicy).Build(),
		Log:     loghelper.New("test"),
		Scheme:  scheme,
		Bundler: fakeBundler,
	}

	// create a bundle
	fakeBundler.bundle = []byte("test")
	err := controller.compileBundle(context.TODO(), testPolicy, nil, "123", controller.Log)
	assert.NilError(t, err)
	assert.Equal(t, conditions.IsTrue(testPolicy, policyv1beta1.BundleCompiledCondition), true)

	bundles := &policyv1beta1.JsPolicyBundleList{}
	err = controller.Client.List(context.TODO(), bundles)
	assert.NilError(t, err)
	assert.Equal(t, len(bundles.Items), 1)
	assert.Equal(t, bundles.Items[0].Name, testPolicy.Name)
	assert.Equal(t, string(bundles.Items[0].Spec.Bundle), string(fakeBundler.bundle))

	// update a bundle
	controller.Client = fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(testPolicy, testPolicyBundle).Build()
	fakeBundler.bundle = []byte("test123")
	err = controller.compileBundle(context.TODO(), testPolicy, testPolicyBundle, "123", controller.Log)
	assert.NilError(t, err)
	assert.Equal(t, conditions.IsTrue(testPolicy, policyv1beta1.BundleCompiledCondition), true)

	bundles = &policyv1beta1.JsPolicyBundleList{}
	err = controller.Client.List(context.TODO(), bundles)
	assert.NilError(t, err)
	assert.Equal(t, len(bundles.Items), 1)
	assert.Equal(t, bundles.Items[0].Name, testPolicy.Name)
	assert.Equal(t, string(bundles.Items[0].Spec.Bundle), string(fakeBundler.bundle))

	// compile failed
	controller.Client = fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(testPolicy).Build()
	fakeBundler.err = errors.New("compile failed")
	err = controller.compileBundle(context.TODO(), testPolicy, nil, "123", controller.Log)
	assert.NilError(t, err)
	assert.Equal(t, conditions.IsFalse(testPolicy, policyv1beta1.BundleCompiledCondition), true)

	bundles = &policyv1beta1.JsPolicyBundleList{}
	err = controller.Client.List(context.TODO(), bundles)
	assert.NilError(t, err)
	assert.Equal(t, len(bundles.Items), 0)

	// compile failed update
	controller.Client = fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(testPolicy, testPolicyBundle).Build()
	fakeBundler.err = errors.New("compile failed")
	err = controller.compileBundle(context.TODO(), testPolicy, testPolicyBundle, "123", controller.Log)
	assert.NilError(t, err)
	assert.Equal(t, conditions.IsFalse(testPolicy, policyv1beta1.BundleCompiledCondition), true)

	bundles = &policyv1beta1.JsPolicyBundleList{}
	err = controller.Client.List(context.TODO(), bundles)
	assert.NilError(t, err)
	assert.Equal(t, len(bundles.Items), 1)

	// bundle missing
	controller.Client = fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(testPolicy).Build()
	err = controller.compileBundle(context.TODO(), testPolicy, nil, "", controller.Log)
	assert.NilError(t, err)
	assert.Equal(t, conditions.IsFalse(testPolicy, policyv1beta1.BundleCompiledCondition), true)

	// bundle not missing anymore
	controller.Client = fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(testPolicy).Build()
	newTestPolicy := testPolicy.DeepCopy()
	newTestPolicy.Status = policyv1beta1.JsPolicyStatus{
		Phase:  policyv1beta1.WebhookPhaseFailed,
		Reason: "BundleJavascript",
	}
	err = controller.compileBundle(context.TODO(), newTestPolicy, testPolicyBundle, "", controller.Log)
	assert.NilError(t, err)
	assert.Equal(t, conditions.IsTrue(newTestPolicy, policyv1beta1.BundleCompiledCondition), true)
}
