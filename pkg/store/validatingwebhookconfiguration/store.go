package validatingwebhookconfiguration

import (
	"context"
	"io/ioutil"
	"path/filepath"

	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	"github.com/loft-sh/jspolicy/pkg/util/certhelper"
	"github.com/loft-sh/jspolicy/pkg/util/clienthelper"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// ValidatingWebhookConfigurationName is the name of the validating webhook configuration
	ValidatingWebhookConfigurationName = "jspolicy"
)

// EnsureValidatingWebhookConfiguration makes sure the validating webhook configuration is up and correct
func EnsureValidatingWebhookConfiguration(ctx context.Context, client client.Client) error {
	config := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	err := client.Get(ctx, types.NamespacedName{Name: ValidatingWebhookConfigurationName}, config)
	if err != nil {
		if kerrors.IsNotFound(err) == false {
			return err
		}

		config.Name = ValidatingWebhookConfigurationName
		err = prepareValidatingWebhookConfiguration(config)
		if err != nil {
			return err
		}

		return client.Create(ctx, config)
	}

	err = prepareValidatingWebhookConfiguration(config)
	if err != nil {
		return err
	}

	return client.Update(ctx, config)
}

func prepareValidatingWebhookConfiguration(config *admissionregistrationv1.ValidatingWebhookConfiguration) error {
	caBundleData, err := ioutil.ReadFile(filepath.Join(certhelper.WebhookCertFolder, "ca.crt"))
	if err != nil {
		return err
	}

	failPolicy := admissionregistrationv1.Fail
	validatePath := "/crds"
	namespace, err := clienthelper.CurrentNamespace()
	if err != nil {
		return err
	}

	sideEffects := admissionregistrationv1.SideEffectClassNone

	clientConfig := admissionregistrationv1.WebhookClientConfig{
		CABundle: caBundleData,
	}
	if url := clienthelper.WebhookURL(); url != "" {
		url = url + validatePath
		clientConfig.URL = &url
	} else {
		clientConfig.Service = &admissionregistrationv1.ServiceReference{
			Namespace: namespace,
			Name:      certhelper.WebhookServiceName,
			Path:      &validatePath,
		}
	}
	config.Webhooks = []admissionregistrationv1.ValidatingWebhook{
		{
			Name:          "jspolicy.jspolicy.com",
			FailurePolicy: &failPolicy,
			SideEffects:   &sideEffects,
			ClientConfig:  clientConfig,
			Rules: []admissionregistrationv1.RuleWithOperations{
				{
					Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update},
					Rule: admissionregistrationv1.Rule{
						APIGroups:   []string{policyv1beta1.SchemeGroupVersion.Group},
						APIVersions: []string{policyv1beta1.SchemeGroupVersion.Version},
						Resources:   []string{"*"},
					},
				},
			},
			AdmissionReviewVersions: []string{admissionregistrationv1.SchemeGroupVersion.Version, "v1beta1"},
		},
	}

	delete(config.Annotations, "cert-manager.io/inject-ca-from")
	return nil
}
