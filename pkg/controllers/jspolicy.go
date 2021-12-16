package controllers

import (
	"context"
	"encoding/json"
	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	"github.com/loft-sh/jspolicy/pkg/bundle"
	"github.com/loft-sh/jspolicy/pkg/constants"
	"github.com/loft-sh/jspolicy/pkg/controller"
	"github.com/loft-sh/jspolicy/pkg/util/clienthelper"
	"github.com/loft-sh/jspolicy/pkg/util/conditions"
	"github.com/loft-sh/jspolicy/pkg/util/hash"
	"github.com/loft-sh/jspolicy/pkg/util/loghelper"
	"github.com/loft-sh/jspolicy/pkg/util/patch"
	"github.com/pkg/errors"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"strconv"
	"sync"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimecontroller "sigs.k8s.io/controller-runtime/pkg/controller"
)

var (
	DefaultBundleTimeout = time.Second * 30

	// constants
	port       = int32(443)
	none       = admissionregistrationv1.SideEffectClassNone
	never      = admissionregistrationv1.NeverReinvocationPolicy
	timeout    = int32(10)
	all        = admissionregistrationv1.AllScopes
	equivalent = admissionregistrationv1.Equivalent
	fail       = admissionregistrationv1.Fail
)

func init() {
	bundleTimeout := os.Getenv("BUNDLE_TIMEOUT")
	if bundleTimeout != "" {
		i, err := strconv.Atoi(bundleTimeout)
		if err != nil {
			klog.Fatalf("Error parsing env variable BUNDLE_TIMEOUT: %v", err)
		}

		DefaultBundleTimeout = time.Duration(i) * time.Second
	}
}

// JsPolicyReconciler reconciles a JsPolicy object
type JsPolicyReconciler struct {
	client.Client
	Log     loghelper.Logger
	Scheme  *runtime.Scheme
	Bundler bundle.JavascriptBundler

	ControllerPolicyManager controller.PolicyManager
	CaBundle                []byte

	controllerPolicyHashMutex sync.Mutex
	controllerPolicyHash      map[string]string
}

type backgroundHashObj struct {
	BundleHash string `json:"bundleHash,omitempty"`

	Resources   []string `json:"resources,omitempty"`
	APIVersions []string `json:"apiVersions,omitempty"`
	APIGroups   []string `json:"apiGroups,omitempty"`
}

// Reconcile reads that state of the cluster for an Account object and makes changes based on the state read
func (r *JsPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, reterr error) {
	log := loghelper.NewFromExisting(r.Log.Base(), req.Name)
	log.Debugf("reconcile started")

	// Retrieve webhook config
	jsPolicy := &policyv1beta1.JsPolicy{}
	if err := r.Get(ctx, req.NamespacedName, jsPolicy); err != nil {
		if kerrors.IsNotFound(err) {
			// delete if it was a background policy
			r.ControllerPolicyManager.Delete(req.Name)
			r.controllerPolicyHashMutex.Lock()
			defer r.controllerPolicyHashMutex.Unlock()

			delete(r.controllerPolicyHash, req.Name)
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, err
	}

	// Hash the javascript
	bundleHash, err := r.hashBundle(jsPolicy.Spec.JavaScript, jsPolicy.Spec.Dependencies)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "hash bundle")
	}

	// Get the bundle resource
	jsPolicyBundle := &policyv1beta1.JsPolicyBundle{}
	err = r.Get(ctx, req.NamespacedName, jsPolicyBundle)
	if err != nil {
		if kerrors.IsNotFound(err) == false {
			return ctrl.Result{}, err
		}

		jsPolicyBundle = nil
	}

	// Initialize the patch helper.
	patchHelper, err := patch.NewHelper(jsPolicy, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	defer func() {
		// Always reconcile the Status.Phase field.
		r.reconcilePhase(jsPolicy, reterr)

		// set bundle hash
		jsPolicy.Status.BundleHash = bundleHash

		// Always attempt to Patch the Cluster object and status after each reconciliation.
		// Patch ObservedGeneration only if the reconciliation completed successfully
		patchOpts := []patch.Option{}
		if reterr == nil {
			patchOpts = append(patchOpts, patch.WithStatusObservedGeneration{})
		}
		if err := patchPolicy(ctx, patchHelper, jsPolicy, patchOpts...); err != nil {
			reterr = utilerrors.NewAggregate([]error{reterr, err})
		}
	}()

	// compile the bundle
	err = r.compileBundle(ctx, jsPolicy, jsPolicyBundle, bundleHash, log)
	if err != nil {
		return ctrl.Result{}, err
	}

	// check if it was failed before
	if conditions.IsFalse(jsPolicy, policyv1beta1.BundleCompiledCondition) {
		err = r.deletePolicyWebhooks(ctx, jsPolicy)
		if err != nil {
			return ctrl.Result{}, err
		}

		r.ControllerPolicyManager.Delete(jsPolicy.Name)
		return ctrl.Result{}, nil
	}

	// if controller policy we make sure we are running it
	if jsPolicy.Spec.Type == policyv1beta1.PolicyTypeController {
		// Bundle was just created, we need to requeue here
		if jsPolicyBundle == nil {
			return ctrl.Result{Requeue: true}, nil
		}

		err = r.syncControllerPolicy(jsPolicy, jsPolicyBundle, bundleHash, log)
		if err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// sync webhook configuration
	err = r.syncWebhook(ctx, jsPolicy)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func patchPolicy(ctx context.Context, patchHelper *patch.Helper, jsPolicy *policyv1beta1.JsPolicy, options ...patch.Option) error {
	// Always update the readyCondition by summarizing the state of other conditions.
	conditions.SetSummary(jsPolicy,
		conditions.WithConditions(
			policyv1beta1.ControllerPolicyReady,
			policyv1beta1.WebhookReady,
			policyv1beta1.BundleCompiledCondition,
		),
	)

	// Patch the object, ignoring conflicts on the conditions owned by this controller.
	// Also, if requested, we are adding additional options like e.g. Patch ObservedGeneration when issuing the
	// patch at the end of the reconcile loop.
	options = append(options,
		patch.WithOwnedConditions{Conditions: []policyv1beta1.ConditionType{
			policyv1beta1.ReadyCondition,
			policyv1beta1.ControllerPolicyReady,
			policyv1beta1.BundleCompiledCondition,
			policyv1beta1.WebhookReady,
		}},
	)
	return patchHelper.Patch(ctx, jsPolicy, options...)
}

func (r *JsPolicyReconciler) reconcilePhase(jsPolicy *policyv1beta1.JsPolicy, err error) {
	jsPolicy.Status.Phase = policyv1beta1.WebhookPhaseSynced
	if err != nil {
		jsPolicy.Status.Phase = policyv1beta1.WebhookPhaseFailed
	}

	// set failed if a condition is errored
	jsPolicy.Status.Reason = ""
	jsPolicy.Status.Message = ""
	for _, c := range jsPolicy.Status.Conditions {
		if c.Status == corev1.ConditionFalse && c.Severity == policyv1beta1.ConditionSeverityError {
			jsPolicy.Status.Phase = policyv1beta1.WebhookPhaseFailed
			jsPolicy.Status.Reason = c.Reason
			jsPolicy.Status.Message = c.Message
			break
		}
	}
}

func (r *JsPolicyReconciler) compileBundle(ctx context.Context, jsPolicy *policyv1beta1.JsPolicy, jsPolicyBundle *policyv1beta1.JsPolicyBundle, bundleHash string, log loghelper.Logger) error {
	// Only sync bundle if there is javascript defined
	if bundleHash != "" {
		// Check if bundle exists
		if jsPolicyBundle == nil {
			// we have to create bundle here
			if !conditions.Has(jsPolicy, policyv1beta1.BundleCompiledCondition) || bundleHash != jsPolicy.Status.BundleHash {
				log.Infof("Bundle jsPolicy %s", jsPolicy.Name)
				jsBundle, err := r.Bundler.Bundle(jsPolicy.Spec.JavaScript, jsPolicy.Spec.Dependencies, DefaultBundleTimeout)
				if err != nil {
					errMsg := err.Error()
					if len(errMsg) > 10000 {
						errMsg = errMsg[:10000] + "..."
					}

					log.Errorf("Error bundling js policy %s: %v", jsPolicy.Name, err)
					conditions.MarkFalse(jsPolicy, policyv1beta1.BundleCompiledCondition, "CompileFailed", policyv1beta1.ConditionSeverityError, "%v", err)
					return nil
				}

				jsPolicyBundle = &policyv1beta1.JsPolicyBundle{
					ObjectMeta: metav1.ObjectMeta{
						Name: jsPolicy.Name,
					},
					Spec: policyv1beta1.JsPolicyBundleSpec{Bundle: jsBundle},
				}
				err = clienthelper.CreateWithOwner(ctx, r.Client, jsPolicyBundle, jsPolicy, r.Scheme)
				if err != nil {
					conditions.Delete(jsPolicy, policyv1beta1.BundleCompiledCondition)
					return err
				}

				conditions.MarkTrue(jsPolicy, policyv1beta1.BundleCompiledCondition)
				return nil
			}
		} else {
			// check if we have to update the bundle
			if !conditions.Has(jsPolicy, policyv1beta1.BundleCompiledCondition) || bundleHash != jsPolicy.Status.BundleHash {
				log.Infof("Bundle changed jsPolicy %s", jsPolicy.Name)
				jsBundle, err := r.Bundler.Bundle(jsPolicy.Spec.JavaScript, jsPolicy.Spec.Dependencies, DefaultBundleTimeout)
				if err != nil {
					errMsg := err.Error()
					if len(errMsg) > 10000 {
						errMsg = errMsg[:10000] + "..."
					}

					log.Errorf("Error bundling js policy %s: %v", jsPolicy.Name, err)
					conditions.MarkFalse(jsPolicy, policyv1beta1.BundleCompiledCondition, "CompileFailed", policyv1beta1.ConditionSeverityError, "%v", err)
					return nil
				}

				jsPolicyBundle.Spec.Bundle = jsBundle
				err = r.Client.Update(ctx, jsPolicyBundle)
				if err != nil {
					conditions.Delete(jsPolicy, policyv1beta1.BundleCompiledCondition)
					return err
				}

				conditions.MarkTrue(jsPolicy, policyv1beta1.BundleCompiledCondition)
				return nil
			}
		}

		return nil
	}

	if jsPolicyBundle == nil {
		conditions.MarkFalse(jsPolicy, policyv1beta1.BundleCompiledCondition, "BundleMissing", policyv1beta1.ConditionSeverityError, "couldn't find js policy bundle, but no javascript provided")
		return nil
	}

	conditions.MarkTrue(jsPolicy, policyv1beta1.BundleCompiledCondition)
	return nil
}

func (r *JsPolicyReconciler) deletePolicyWebhooks(ctx context.Context, jsPolicy *policyv1beta1.JsPolicy) error {
	// delete all validating webhooks
	validatingWebhookList := &admissionregistrationv1.ValidatingWebhookConfigurationList{}
	err := r.List(ctx, validatingWebhookList, client.MatchingFields{constants.IndexByJsPolicy: jsPolicy.Name})
	if err != nil {
		return err
	}
	for _, vw := range validatingWebhookList.Items {
		r.Log.Infof("Delete validating webhook %s", vw.Name)
		err = r.Delete(ctx, &vw)
		if err != nil && kerrors.IsNotFound(err) == false {
			return err
		}
	}

	// delete all mutating webhooks
	mutatingWebhookList := &admissionregistrationv1.MutatingWebhookConfigurationList{}
	err = r.List(ctx, mutatingWebhookList, client.MatchingFields{constants.IndexByJsPolicy: jsPolicy.Name})
	if err != nil {
		return err
	}
	for _, vw := range mutatingWebhookList.Items {
		r.Log.Infof("Delete mutating webhook %s", vw.Name)
		err = r.Delete(ctx, &vw)
		if err != nil && kerrors.IsNotFound(err) == false {
			return err
		}
	}

	return nil
}

func (r *JsPolicyReconciler) syncControllerPolicy(jsPolicy *policyv1beta1.JsPolicy, jsPolicyBundle *policyv1beta1.JsPolicyBundle, bundleHash string, log loghelper.Logger) error {
	if bundleHash == "" {
		// this is not a javascript hash, but it will work as well
		bundleHash = hash.String(string(jsPolicyBundle.Spec.Bundle))
	}

	bundleObject, err := json.Marshal(&backgroundHashObj{
		BundleHash:  bundleHash,
		Resources:   jsPolicy.Spec.Resources,
		APIGroups:   jsPolicy.Spec.APIGroups,
		APIVersions: jsPolicy.Spec.APIVersions,
	})
	if err != nil {
		return errors.Wrap(err, "hash controller policy object")
	}

	completeBundleHash := hash.String(string(bundleObject))
	r.controllerPolicyHashMutex.Lock()
	oldHash, ok := r.controllerPolicyHash[jsPolicy.Name]
	r.controllerPolicyHashMutex.Unlock()
	if !ok || oldHash != completeBundleHash {
		log.Infof("Update controller policy %s", jsPolicy.Name)
		r.controllerPolicyHashMutex.Lock()
		r.controllerPolicyHash[jsPolicy.Name] = completeBundleHash
		r.controllerPolicyHashMutex.Unlock()
		err := r.ControllerPolicyManager.Update(jsPolicy, true)
		if err != nil {
			log.Infof("Error starting controller policy: %v", err)
			r.ControllerPolicyManager.Delete(jsPolicy.Name)
			conditions.MarkFalse(jsPolicy, policyv1beta1.ControllerPolicyReady, "InitControllerPolicy", policyv1beta1.ConditionSeverityError, "error starting controller policy: %v", err)
			return nil
		}
	} else if conditions.IsFalse(jsPolicy, policyv1beta1.ControllerPolicyReady) {
		return nil
	}

	conditions.MarkTrue(jsPolicy, policyv1beta1.ControllerPolicyReady)
	return nil
}

func (r *JsPolicyReconciler) hashBundle(javascript string, dependencies map[string]string) (string, error) {
	if javascript == "" {
		return "", nil
	}

	marshalled, err := json.Marshal(map[string]interface{}{
		"javascript":   javascript,
		"dependencies": dependencies,
	})
	if err != nil {
		return "", err
	}

	return hash.String(string(marshalled)), nil
}

func (r *JsPolicyReconciler) syncWebhook(ctx context.Context, jsPolicy *policyv1beta1.JsPolicy) error {
	if jsPolicy.Spec.Type == policyv1beta1.PolicyTypeController {
		return nil
	}

	// delete webhooks that are in the wrong category
	if jsPolicy.Spec.Type == policyv1beta1.PolicyTypeMutating {
		// delete all validating webhooks
		validatingWebhookList := &admissionregistrationv1.ValidatingWebhookConfigurationList{}
		err := r.List(ctx, validatingWebhookList, client.MatchingFields{constants.IndexByJsPolicy: jsPolicy.Name})
		if err != nil {
			return err
		}
		for _, vw := range validatingWebhookList.Items {
			r.Log.Infof("Delete validating webhook %s", vw.Name)
			err = r.Delete(ctx, &vw)
			if err != nil && kerrors.IsNotFound(err) == false {
				return err
			}
		}

		// check if there is an owned mutating webhook configuration
		mutatingWebhookList := &admissionregistrationv1.MutatingWebhookConfigurationList{}
		err = r.List(ctx, mutatingWebhookList, client.MatchingFields{constants.IndexByJsPolicy: jsPolicy.Name})
		if err != nil {
			return err
		}

		// create or update webhook?
		webhook := &admissionregistrationv1.MutatingWebhookConfiguration{}
		if len(mutatingWebhookList.Items) > 0 {
			webhook = &mutatingWebhookList.Items[0]
		}

		// delete other webhooks
		if len(mutatingWebhookList.Items) > 1 {
			for i, vw := range mutatingWebhookList.Items {
				if i == 0 {
					continue
				}

				r.Log.Infof("Delete mutating webhook %s", vw.Name)
				err = r.Delete(ctx, &vw)
				if err != nil && kerrors.IsNotFound(err) == false {
					return err
				}
			}
		}

		// sync the webhook configuration
		err = r.syncMutatingWebhookConfiguration(ctx, jsPolicy, webhook)
		if err != nil {
			conditions.MarkFalse(jsPolicy, policyv1beta1.WebhookReady, "SyncFailed", policyv1beta1.ConditionSeverityError, "%v", err)
			return err
		}

		conditions.MarkTrue(jsPolicy, policyv1beta1.WebhookReady)
		return nil
	}

	// delete all mutating webhooks
	mutatingWebhookList := &admissionregistrationv1.MutatingWebhookConfigurationList{}
	err := r.List(ctx, mutatingWebhookList, client.MatchingFields{constants.IndexByJsPolicy: jsPolicy.Name})
	if err != nil {
		return err
	}
	for _, vw := range mutatingWebhookList.Items {
		r.Log.Infof("Delete mutating webhook %s", vw.Name)
		err = r.Delete(ctx, &vw)
		if err != nil && kerrors.IsNotFound(err) == false {
			return err
		}
	}

	// check if there is an owned validating webhook configuration
	validatingWebhookList := &admissionregistrationv1.ValidatingWebhookConfigurationList{}
	err = r.List(ctx, validatingWebhookList, client.MatchingFields{constants.IndexByJsPolicy: jsPolicy.Name})
	if err != nil {
		return err
	}

	// create or update webhook?
	webhook := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	if len(validatingWebhookList.Items) > 0 {
		webhook = &validatingWebhookList.Items[0]
	}

	// delete other webhooks
	if len(validatingWebhookList.Items) > 1 {
		for i, vw := range validatingWebhookList.Items {
			if i == 0 {
				continue
			}

			r.Log.Infof("Delete validating webhook %s", vw.Name)
			err = r.Delete(ctx, &vw)
			if err != nil && kerrors.IsNotFound(err) == false {
				return err
			}
		}
	}

	// sync the webhook configuration
	err = r.syncValidatingWebhookConfiguration(ctx, jsPolicy, webhook)
	if err != nil {
		conditions.MarkFalse(jsPolicy, policyv1beta1.WebhookReady, "SyncFailed", policyv1beta1.ConditionSeverityError, "%v", err)
		return err
	}

	conditions.MarkTrue(jsPolicy, policyv1beta1.WebhookReady)
	return nil
}

func (r *JsPolicyReconciler) syncMutatingWebhookConfiguration(ctx context.Context, jsPolicy *policyv1beta1.JsPolicy, webhook *admissionregistrationv1.MutatingWebhookConfiguration) error {
	namespace, err := clienthelper.CurrentNamespace()
	if err != nil {
		return err
	}

	// copy the webhook
	originalWebhook := webhook.DeepCopy()

	// should reset webhook?
	if len(webhook.Webhooks) != 1 {
		webhook.Webhooks = []admissionregistrationv1.MutatingWebhook{{}}
	}

	// Ensure webhook fields
	webhook.Webhooks[0].Name = jsPolicy.Name
	path := "/policy/" + jsPolicy.Name
	webhook.Webhooks[0].ClientConfig.Service = &admissionregistrationv1.ServiceReference{
		Name:      clienthelper.ServiceName(),
		Namespace: namespace,
		Path:      &path,
		Port:      &port,
	}
	webhook.Webhooks[0].ClientConfig.CABundle = r.CaBundle
	if len(webhook.Webhooks[0].Rules) != 1 {
		webhook.Webhooks[0].Rules = []admissionregistrationv1.RuleWithOperations{{}}
	}
	webhook.Webhooks[0].Rules[0].Operations = jsPolicy.Spec.Operations
	webhook.Webhooks[0].Rules[0].Rule = admissionregistrationv1.Rule{
		APIGroups:   jsPolicy.Spec.APIGroups,
		APIVersions: jsPolicy.Spec.APIVersions,
		Resources:   jsPolicy.Spec.Resources,
		Scope:       jsPolicy.Spec.Scope,
	}
	if len(webhook.Webhooks[0].Rules[0].APIGroups) == 0 {
		webhook.Webhooks[0].Rules[0].APIGroups = []string{"*"}
	}
	if len(webhook.Webhooks[0].Rules[0].APIVersions) == 0 {
		webhook.Webhooks[0].Rules[0].APIVersions = []string{"*"}
	}
	if webhook.Webhooks[0].Rules[0].Scope == nil {
		webhook.Webhooks[0].Rules[0].Scope = &all
	}

	webhook.Webhooks[0].FailurePolicy = jsPolicy.Spec.FailurePolicy
	if webhook.Webhooks[0].FailurePolicy == nil {
		webhook.Webhooks[0].FailurePolicy = &fail
	}
	webhook.Webhooks[0].NamespaceSelector = jsPolicy.Spec.NamespaceSelector
	if webhook.Webhooks[0].NamespaceSelector == nil {
		webhook.Webhooks[0].NamespaceSelector = &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{
					Key:      "control-plane",
					Operator: metav1.LabelSelectorOpDoesNotExist,
				},
			},
		}
	}
	webhook.Webhooks[0].ObjectSelector = jsPolicy.Spec.ObjectSelector
	if webhook.Webhooks[0].ObjectSelector == nil {
		webhook.Webhooks[0].ObjectSelector = &metav1.LabelSelector{}
	}
	webhook.Webhooks[0].TimeoutSeconds = jsPolicy.Spec.TimeoutSeconds
	if webhook.Webhooks[0].TimeoutSeconds == nil {
		webhook.Webhooks[0].TimeoutSeconds = &timeout
	}
	webhook.Webhooks[0].MatchPolicy = jsPolicy.Spec.MatchPolicy
	if webhook.Webhooks[0].MatchPolicy == nil {
		webhook.Webhooks[0].MatchPolicy = &equivalent
	}
	if webhook.Webhooks[0].SideEffects == nil {
		webhook.Webhooks[0].SideEffects = &none
	}
	if webhook.Webhooks[0].ReinvocationPolicy == nil {
		webhook.Webhooks[0].ReinvocationPolicy = &never
	}
	if len(webhook.Webhooks[0].AdmissionReviewVersions) == 0 {
		webhook.Webhooks[0].AdmissionReviewVersions = []string{"v1"}
	}

	// check if create
	if webhook.Name == "" {
		webhook.GenerateName = jsPolicy.Name + "-"
		r.Log.Infof("Create mutating webhook %s", jsPolicy.Name)
		return clienthelper.CreateWithOwner(ctx, r.Client, webhook, jsPolicy, r.Scheme)
	}

	// we only update if there was really a change
	mergePatch := client.MergeFrom(originalWebhook)
	mergeData, err := mergePatch.Data(webhook)
	if err != nil {
		return err
	} else if string(mergeData) == "{}" {
		return nil
	}

	// update the webhook
	r.Log.Infof("Patching mutating webhook %s with %s", webhook.Name, string(mergeData))
	return r.Patch(ctx, webhook, mergePatch)
}

func (r *JsPolicyReconciler) syncValidatingWebhookConfiguration(ctx context.Context, jsPolicy *policyv1beta1.JsPolicy, webhook *admissionregistrationv1.ValidatingWebhookConfiguration) error {
	namespace, err := clienthelper.CurrentNamespace()
	if err != nil {
		return err
	}

	// copy the webhook
	originalWebhook := webhook.DeepCopy()

	// should reset webhook?
	if len(webhook.Webhooks) != 1 {
		webhook.Webhooks = []admissionregistrationv1.ValidatingWebhook{{}}
	}

	// Ensure webhook fields
	webhook.Webhooks[0].Name = jsPolicy.Name
	path := "/policy/" + jsPolicy.Name
	webhook.Webhooks[0].ClientConfig.Service = &admissionregistrationv1.ServiceReference{
		Name:      clienthelper.ServiceName(),
		Namespace: namespace,
		Path:      &path,
		Port:      &port,
	}
	webhook.Webhooks[0].ClientConfig.CABundle = r.CaBundle
	if len(webhook.Webhooks[0].Rules) != 1 {
		webhook.Webhooks[0].Rules = []admissionregistrationv1.RuleWithOperations{{}}
	}
	webhook.Webhooks[0].Rules[0].Operations = jsPolicy.Spec.Operations
	webhook.Webhooks[0].Rules[0].Rule = admissionregistrationv1.Rule{
		APIGroups:   jsPolicy.Spec.APIGroups,
		APIVersions: jsPolicy.Spec.APIVersions,
		Resources:   jsPolicy.Spec.Resources,
		Scope:       jsPolicy.Spec.Scope,
	}
	if len(webhook.Webhooks[0].Rules[0].APIGroups) == 0 {
		webhook.Webhooks[0].Rules[0].APIGroups = []string{"*"}
	}
	if len(webhook.Webhooks[0].Rules[0].APIVersions) == 0 {
		webhook.Webhooks[0].Rules[0].APIVersions = []string{"*"}
	}
	if webhook.Webhooks[0].Rules[0].Scope == nil {
		webhook.Webhooks[0].Rules[0].Scope = &all
	}

	webhook.Webhooks[0].FailurePolicy = jsPolicy.Spec.FailurePolicy
	if webhook.Webhooks[0].FailurePolicy == nil {
		webhook.Webhooks[0].FailurePolicy = &fail
	}
	webhook.Webhooks[0].NamespaceSelector = jsPolicy.Spec.NamespaceSelector
	if webhook.Webhooks[0].NamespaceSelector == nil {
		webhook.Webhooks[0].NamespaceSelector = &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{
					Key:      "control-plane",
					Operator: metav1.LabelSelectorOpDoesNotExist,
				},
			},
		}
	}
	webhook.Webhooks[0].ObjectSelector = jsPolicy.Spec.ObjectSelector
	if webhook.Webhooks[0].ObjectSelector == nil {
		webhook.Webhooks[0].ObjectSelector = &metav1.LabelSelector{}
	}
	webhook.Webhooks[0].TimeoutSeconds = jsPolicy.Spec.TimeoutSeconds
	if webhook.Webhooks[0].TimeoutSeconds == nil {
		webhook.Webhooks[0].TimeoutSeconds = &timeout
	}
	webhook.Webhooks[0].MatchPolicy = jsPolicy.Spec.MatchPolicy
	if webhook.Webhooks[0].MatchPolicy == nil {
		webhook.Webhooks[0].MatchPolicy = &equivalent
	}
	if webhook.Webhooks[0].SideEffects == nil {
		webhook.Webhooks[0].SideEffects = &none
	}
	if len(webhook.Webhooks[0].AdmissionReviewVersions) == 0 {
		webhook.Webhooks[0].AdmissionReviewVersions = []string{"v1"}
	}

	// check if create
	if webhook.Name == "" {
		webhook.GenerateName = jsPolicy.Name + "-"
		r.Log.Infof("Create validating webhook %s", jsPolicy.Name)
		return clienthelper.CreateWithOwner(ctx, r.Client, webhook, jsPolicy, r.Scheme)
	}

	// we only update if there was really a change
	mergePatch := client.MergeFrom(originalWebhook)
	mergeData, err := mergePatch.Data(webhook)
	if err != nil {
		return err
	} else if string(mergeData) == "{}" {
		return nil
	}

	// update the webhook
	r.Log.Infof("Patching validating webhook %s with %s", webhook.Name, string(mergeData))
	return r.Patch(ctx, webhook, mergePatch)
}

// SetupWithManager adds the controller to the manager
func (r *JsPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(runtimecontroller.Options{
			MaxConcurrentReconciles: 10,
		}).
		Owns(&admissionregistrationv1.ValidatingWebhookConfiguration{}).
		Owns(&admissionregistrationv1.MutatingWebhookConfiguration{}).
		Watches(&source.Kind{Type: &policyv1beta1.JsPolicyBundle{}}, &bundleHandler{client: mgr.GetClient()}).
		For(&policyv1beta1.JsPolicy{}).
		Complete(r)
}
