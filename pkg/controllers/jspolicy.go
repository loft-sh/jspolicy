package controllers

import (
	"context"
	"encoding/json"
	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	"github.com/loft-sh/jspolicy/pkg/bundle"
	"github.com/loft-sh/jspolicy/pkg/constants"
	"github.com/loft-sh/jspolicy/pkg/controller"
	"github.com/loft-sh/jspolicy/pkg/util/clienthelper"
	"github.com/loft-sh/jspolicy/pkg/util/hash"
	"github.com/loft-sh/jspolicy/pkg/util/loghelper"
	"github.com/pkg/errors"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"strconv"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	DefaultBundleTimeout = time.Second * 30
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

	CaBundle []byte

	controllerPolicyHash map[string]string
}

type backgroundHashObj struct {
	BundleHash string `json:"bundleHash,omitempty"`

	Resources   []string `json:"resources,omitempty"`
	APIVersions []string `json:"apiVersions,omitempty"`
	APIGroups   []string `json:"apiGroups,omitempty"`
}

// Reconcile reads that state of the cluster for an Account object and makes changes based on the state read
func (r *JsPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := loghelper.NewFromExisting(r.Log, req.Name)
	log.Debugf("reconcile started")

	// Retrieve webhook config
	jsPolicy := &policyv1beta1.JsPolicy{}
	if err := r.Get(ctx, req.NamespacedName, jsPolicy); err != nil {
		if kerrors.IsNotFound(err) {
			// delete if it was a background policy
			r.ControllerPolicyManager.Delete(req.Name)
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

	// compile the bundle
	newStatus, err := r.compileBundle(ctx, jsPolicy, jsPolicyBundle, bundleHash, log)
	if err != nil {
		return ctrl.Result{}, err
	} else if newStatus != nil {
		jsPolicy.Status = *newStatus
	}

	// check if it was failed before
	if jsPolicy.Status.Phase == policyv1beta1.WebhookPhaseFailed && jsPolicy.Status.Reason == "BundleJavascript" {
		_ = r.deletePolicyWebhooks(ctx, jsPolicy)
		r.ControllerPolicyManager.Delete(jsPolicy.Name)
		return ctrl.Result{}, r.Client.Status().Update(ctx, jsPolicy)
	}

	// if controller policy we make sure we are running it
	if jsPolicy.Spec.Type == policyv1beta1.PolicyTypeController {
		if jsPolicyBundle == nil {
			return ctrl.Result{Requeue: true}, nil
		}

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
			return ctrl.Result{}, errors.Wrap(err, "hash controller policy object")
		}

		completeBundleHash := hash.String(string(bundleObject))
		oldHash, ok := r.controllerPolicyHash[jsPolicy.Name]
		if !ok || oldHash != completeBundleHash {
			log.Infof("Update controller policy %s", jsPolicy.Name)
			r.controllerPolicyHash[jsPolicy.Name] = completeBundleHash
			err := r.ControllerPolicyManager.Update(jsPolicy, true)
			if err != nil {
				log.Infof("Error starting controller policy: %v", err)
				jsPolicy.Status.Phase = policyv1beta1.WebhookPhaseFailed
				jsPolicy.Status.Reason = "StartController"
				jsPolicy.Status.Message = err.Error()
				r.ControllerPolicyManager.Delete(jsPolicy.Name)
				return ctrl.Result{}, r.Client.Status().Update(ctx, jsPolicy)
			}
		} else if jsPolicy.Status.Phase == policyv1beta1.WebhookPhaseFailed && jsPolicy.Status.Reason == "StartController" {
			return ctrl.Result{}, nil
		}

		// check if it was failed before
		if jsPolicy.Status.Phase == policyv1beta1.WebhookPhaseFailed || jsPolicy.Status.Phase == "" {
			jsPolicy.Status.Phase = policyv1beta1.WebhookPhaseSynced
			jsPolicy.Status.Reason = ""
			jsPolicy.Status.Message = ""
		}

		return ctrl.Result{}, r.Client.Status().Update(ctx, jsPolicy)
	}

	// sync webhook configuration
	err = r.syncWebhook(ctx, jsPolicy)
	if err != nil {
		jsPolicy.Status.Phase = policyv1beta1.WebhookPhaseFailed
		jsPolicy.Status.Reason = "SyncWebhook"
		jsPolicy.Status.Message = err.Error()
		log.Errorf("Error syncing js policy %s: %v", jsPolicy.Name, err)
		return ctrl.Result{}, r.Status().Update(ctx, jsPolicy)
	}

	// check if it was failed before
	if jsPolicy.Status.Phase == policyv1beta1.WebhookPhaseFailed || jsPolicy.Status.Phase == "" {
		jsPolicy.Status.Phase = policyv1beta1.WebhookPhaseSynced
		jsPolicy.Status.Reason = ""
		jsPolicy.Status.Message = ""
	}

	return ctrl.Result{}, r.Client.Status().Update(ctx, jsPolicy)
}

func (r *JsPolicyReconciler) compileBundle(ctx context.Context, jsPolicy *policyv1beta1.JsPolicy, jsPolicyBundle *policyv1beta1.JsPolicyBundle, bundleHash string, log loghelper.Logger) (*policyv1beta1.JsPolicyStatus, error) {
	// Only sync bundle if there is javascript defined
	if bundleHash != "" {
		// Check if bundle exists
		if jsPolicyBundle == nil {
			// we have to create bundle here
			if bundleHash != jsPolicy.Status.BundleHash || jsPolicy.Status.Phase == policyv1beta1.WebhookPhaseSynced {
				log.Infof("Bundle jsPolicy %s", jsPolicy.Name)
				jsBundle, err := r.Bundler.Bundle(jsPolicy.Spec.JavaScript, jsPolicy.Spec.Dependencies, DefaultBundleTimeout)
				if err != nil {
					errMsg := err.Error()
					if len(errMsg) > 10000 {
						errMsg = errMsg[:10000] + "..."
					}

					log.Errorf("Error bundling js policy %s: %v", jsPolicy.Name, err)
					return failedCompileStatus(errMsg, bundleHash), nil
				}

				jsPolicyBundle = &policyv1beta1.JsPolicyBundle{
					ObjectMeta: metav1.ObjectMeta{
						Name: jsPolicy.Name,
					},
					Spec: policyv1beta1.JsPolicyBundleSpec{Bundle: jsBundle},
				}
				err = clienthelper.CreateWithOwner(ctx, r.Client, jsPolicyBundle, jsPolicy, r.Scheme)
				if err != nil {
					return nil, err
				}

				return &policyv1beta1.JsPolicyStatus{
					Phase:      policyv1beta1.WebhookPhaseSynced,
					BundleHash: bundleHash,
				}, nil
			}
		} else {
			// check if we have to update the bundle
			if bundleHash != jsPolicy.Status.BundleHash {
				log.Infof("Bundle changed jsPolicy %s", jsPolicy.Name)
				jsBundle, err := r.Bundler.Bundle(jsPolicy.Spec.JavaScript, jsPolicy.Spec.Dependencies, DefaultBundleTimeout)
				if err != nil {
					errMsg := err.Error()
					if len(errMsg) > 10000 {
						errMsg = errMsg[:10000] + "..."
					}

					log.Errorf("Error bundling js policy %s: %v", jsPolicy.Name, err)
					return failedCompileStatus(errMsg, bundleHash), nil
				}

				jsPolicyBundle.Spec.Bundle = jsBundle
				err = r.Client.Update(ctx, jsPolicyBundle)
				if err != nil {
					return nil, err
				}

				return &policyv1beta1.JsPolicyStatus{
					Phase:      policyv1beta1.WebhookPhaseSynced,
					BundleHash: bundleHash,
				}, nil
			}
		}
	} else {
		if jsPolicyBundle == nil {
			return failedCompileStatus("couldn't find js policy bundle", bundleHash), nil
		} else if jsPolicy.Status.Phase == policyv1beta1.WebhookPhaseFailed && jsPolicy.Status.Reason == "BundleJavascript" {
			return &policyv1beta1.JsPolicyStatus{
				Phase: policyv1beta1.WebhookPhaseSynced,
			}, nil
		}
	}

	return nil, nil
}

func failedCompileStatus(message, hash string) *policyv1beta1.JsPolicyStatus {
	return &policyv1beta1.JsPolicyStatus{
		Phase:      policyv1beta1.WebhookPhaseFailed,
		Reason:     "BundleJavascript",
		Message:    message,
		BundleHash: hash,
	}
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
			return err
		}

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
		return err
	}

	return nil
}

func (r *JsPolicyReconciler) syncMutatingWebhookConfiguration(ctx context.Context, jsPolicy *policyv1beta1.JsPolicy, webhook *admissionregistrationv1.MutatingWebhookConfiguration) error {
	namespace, err := clienthelper.CurrentNamespace()
	if err != nil {
		return err
	}

	// client config
	port := int32(443)
	clientConfig := admissionregistrationv1.WebhookClientConfig{
		Service: &admissionregistrationv1.ServiceReference{
			Name:      clienthelper.ServiceName(),
			Namespace: namespace,
			Port:      &port,
		},
		CABundle: r.CaBundle,
	}

	// original webhook
	originalWebhook := webhook.DeepCopy()

	none := admissionregistrationv1.SideEffectClassNone
	never := admissionregistrationv1.NeverReinvocationPolicy
	webhook.Webhooks = []admissionregistrationv1.MutatingWebhook{{
		Name:               jsPolicy.Name,
		NamespaceSelector:  jsPolicy.Spec.NamespaceSelector,
		ObjectSelector:     jsPolicy.Spec.ObjectSelector,
		FailurePolicy:      jsPolicy.Spec.FailurePolicy,
		TimeoutSeconds:     jsPolicy.Spec.TimeoutSeconds,
		MatchPolicy:        jsPolicy.Spec.MatchPolicy,
		SideEffects:        &none,
		ReinvocationPolicy: &never,
		Rules: []admissionregistrationv1.RuleWithOperations{
			{
				Operations: jsPolicy.Spec.Operations,
				Rule: admissionregistrationv1.Rule{
					APIGroups:   jsPolicy.Spec.APIGroups,
					APIVersions: jsPolicy.Spec.APIVersions,
					Resources:   jsPolicy.Spec.Resources,
					Scope:       jsPolicy.Spec.Scope,
				},
			},
		},
	}}

	// set defaults
	if len(webhook.Webhooks[0].Rules[0].APIGroups) == 0 {
		webhook.Webhooks[0].Rules[0].APIGroups = []string{"*"}
	}
	if len(webhook.Webhooks[0].Rules[0].APIVersions) == 0 {
		webhook.Webhooks[0].Rules[0].APIVersions = []string{"*"}
	}
	if webhook.Webhooks[0].FailurePolicy == nil {
		fail := admissionregistrationv1.Fail
		webhook.Webhooks[0].FailurePolicy = &fail
	}
	if webhook.Webhooks[0].NamespaceSelector == nil {
		webhook.Webhooks[0].NamespaceSelector = &metav1.LabelSelector{}
	}
	if webhook.Webhooks[0].ObjectSelector == nil {
		webhook.Webhooks[0].ObjectSelector = &metav1.LabelSelector{}
	}
	if webhook.Webhooks[0].TimeoutSeconds == nil {
		timeout := int32(10)
		webhook.Webhooks[0].TimeoutSeconds = &timeout
	}
	if webhook.Webhooks[0].MatchPolicy == nil {
		equivalent := admissionregistrationv1.Equivalent
		webhook.Webhooks[0].MatchPolicy = &equivalent
	}
	if webhook.Webhooks[0].Rules[0].Scope == nil {
		all := admissionregistrationv1.AllScopes
		webhook.Webhooks[0].Rules[0].Scope = &all
	}

	// apply client config
	for i := range webhook.Webhooks {
		path := "/policy/" + jsPolicy.Name
		webhook.Webhooks[i].ClientConfig = clientConfig
		webhook.Webhooks[i].ClientConfig.Service.Path = &path
		webhook.Webhooks[i].AdmissionReviewVersions = []string{"v1"}
	}

	// check if create
	if webhook.Name == "" {
		webhook.GenerateName = jsPolicy.Name + "-"
		r.Log.Infof("Create mutating webhook %s", jsPolicy.Name)
		return clienthelper.CreateWithOwner(ctx, r.Client, webhook, jsPolicy, r.Scheme)
	}

	// we only update if there was really a change
	patch, err := client.MergeFrom(originalWebhook).Data(webhook)
	if err != nil {
		return err
	} else if string(patch) == "{}" {
		return nil
	}

	// update the webhook
	r.Log.Infof("Update mutating webhook %s", webhook.Name)
	return r.Update(ctx, webhook)
}

func (r *JsPolicyReconciler) syncValidatingWebhookConfiguration(ctx context.Context, jsPolicy *policyv1beta1.JsPolicy, webhook *admissionregistrationv1.ValidatingWebhookConfiguration) error {
	namespace, err := clienthelper.CurrentNamespace()
	if err != nil {
		return err
	}

	// client config
	port := int32(443)
	clientConfig := admissionregistrationv1.WebhookClientConfig{
		Service: &admissionregistrationv1.ServiceReference{
			Name:      clienthelper.ServiceName(),
			Namespace: namespace,
			Port:      &port,
		},
		CABundle: r.CaBundle,
	}

	// original webhook
	originalWebhook := webhook.DeepCopy()

	none := admissionregistrationv1.SideEffectClassNone
	webhook.Webhooks = []admissionregistrationv1.ValidatingWebhook{{
		Name:              jsPolicy.Name,
		NamespaceSelector: jsPolicy.Spec.NamespaceSelector,
		ObjectSelector:    jsPolicy.Spec.ObjectSelector,
		FailurePolicy:     jsPolicy.Spec.FailurePolicy,
		TimeoutSeconds:    jsPolicy.Spec.TimeoutSeconds,
		MatchPolicy:       jsPolicy.Spec.MatchPolicy,
		SideEffects:       &none,
		Rules: []admissionregistrationv1.RuleWithOperations{
			{
				Operations: jsPolicy.Spec.Operations,
				Rule: admissionregistrationv1.Rule{
					APIGroups:   jsPolicy.Spec.APIGroups,
					APIVersions: jsPolicy.Spec.APIVersions,
					Resources:   jsPolicy.Spec.Resources,
					Scope:       jsPolicy.Spec.Scope,
				},
			},
		},
	}}

	// set defaults
	if len(webhook.Webhooks[0].Rules[0].APIGroups) == 0 {
		webhook.Webhooks[0].Rules[0].APIGroups = []string{"*"}
	}
	if len(webhook.Webhooks[0].Rules[0].APIVersions) == 0 {
		webhook.Webhooks[0].Rules[0].APIVersions = []string{"*"}
	}
	if webhook.Webhooks[0].FailurePolicy == nil {
		fail := admissionregistrationv1.Fail
		webhook.Webhooks[0].FailurePolicy = &fail
	}
	if webhook.Webhooks[0].NamespaceSelector == nil {
		webhook.Webhooks[0].NamespaceSelector = &metav1.LabelSelector{}
	}
	if webhook.Webhooks[0].ObjectSelector == nil {
		webhook.Webhooks[0].ObjectSelector = &metav1.LabelSelector{}
	}
	if webhook.Webhooks[0].TimeoutSeconds == nil {
		timeout := int32(10)
		webhook.Webhooks[0].TimeoutSeconds = &timeout
	}
	if webhook.Webhooks[0].MatchPolicy == nil {
		equivalent := admissionregistrationv1.Equivalent
		webhook.Webhooks[0].MatchPolicy = &equivalent
	}
	if webhook.Webhooks[0].Rules[0].Scope == nil {
		all := admissionregistrationv1.AllScopes
		webhook.Webhooks[0].Rules[0].Scope = &all
	}

	// apply client config
	for i := range webhook.Webhooks {
		path := "/policy/" + jsPolicy.Name
		webhook.Webhooks[i].ClientConfig = clientConfig
		webhook.Webhooks[i].ClientConfig.Service.Path = &path
		webhook.Webhooks[i].AdmissionReviewVersions = []string{"v1"}
	}

	// check if create
	if webhook.Name == "" {
		webhook.GenerateName = jsPolicy.Name + "-"
		r.Log.Infof("Create validating webhook %s", jsPolicy.Name)
		return clienthelper.CreateWithOwner(ctx, r.Client, webhook, jsPolicy, r.Scheme)
	}

	// we only update if there was really a change
	patch, err := client.MergeFrom(originalWebhook).Data(webhook)
	if err != nil {
		return err
	} else if string(patch) == "{}" {
		return nil
	}

	// update the webhook
	r.Log.Infof("Update validating webhook %s", webhook.Name)
	return r.Update(ctx, webhook)
}

// SetupWithManager adds the controller to the manager
func (r *JsPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Owns(&admissionregistrationv1.ValidatingWebhookConfiguration{}).
		Owns(&admissionregistrationv1.MutatingWebhookConfiguration{}).
		Watches(&source.Kind{Type: &policyv1beta1.JsPolicyBundle{}}, &bundleHandler{client: mgr.GetClient()}).
		For(&policyv1beta1.JsPolicy{}).
		Complete(r)
}
