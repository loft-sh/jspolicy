package controllers

import (
	"context"
	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	"github.com/loft-sh/jspolicy/pkg/constants"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// AddManagerIndices adds the needed manager indices for faster listing of resources
func AddManagerIndices(indexer client.FieldIndexer) error {
	// Index by owner
	if err := indexer.IndexField(context.TODO(), &admissionregistrationv1.ValidatingWebhookConfiguration{}, constants.IndexByJsPolicy, func(rawObj client.Object) []string {
		// grab the object, extract the owner...
		cr := rawObj.(*admissionregistrationv1.ValidatingWebhookConfiguration)
		owner := metav1.GetControllerOf(cr)
		if owner == nil || owner.APIVersion != policyv1beta1.GroupVersion.String() || owner.Kind != "JsPolicy" {
			return nil
		}

		return []string{owner.Name}
	}); err != nil {
		return err
	}

	// Index by owner
	if err := indexer.IndexField(context.TODO(), &admissionregistrationv1.MutatingWebhookConfiguration{}, constants.IndexByJsPolicy, func(rawObj client.Object) []string {
		// grab the object, extract the owner...
		cr := rawObj.(*admissionregistrationv1.MutatingWebhookConfiguration)
		owner := metav1.GetControllerOf(cr)
		if owner == nil || owner.APIVersion != policyv1beta1.GroupVersion.String() || owner.Kind != "JsPolicy" {
			return nil
		}

		return []string{owner.Name}
	}); err != nil {
		return err
	}

	return nil
}
