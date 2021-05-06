package clienthelper

import (
	"context"
	"io/ioutil"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func ServiceName() string {
	if os.Getenv("JS_POLICY_SERVICE_NAME") != "" {
		return os.Getenv("JS_POLICY_SERVICE_NAME")
	}

	return "jspolicy"
}

func CurrentNamespace() (string, error) {
	envNamespace := os.Getenv("KUBE_NAMESPACE")
	if envNamespace != "" {
		return envNamespace, nil
	}

	namespace, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", err
	}

	return string(namespace), nil
}

func CreateWithOwner(ctx context.Context, kubeClient client.Client, object runtime.Object, owner metav1.Object, scheme *runtime.Scheme) error {
	accessor, err := meta.Accessor(object)
	if err != nil {
		return err
	}
	typeAccessor, err := meta.TypeAccessor(object)
	if err != nil {
		return err
	}

	// Set owner controller
	err = ctrl.SetControllerReference(owner, accessor, scheme)
	if err != nil {
		return err
	}

	err = kubeClient.Create(ctx, object.(client.Object))
	if err != nil {
		return err
	}

	klog.Info("created " + typeAccessor.GetKind() + "  " + accessor.GetName())
	return nil
}
