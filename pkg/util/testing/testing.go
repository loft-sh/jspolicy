package testing

import (
	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
)

// NewScheme creates a new scheme
func NewScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = policyv1beta1.AddToScheme(scheme)
	return scheme
}
