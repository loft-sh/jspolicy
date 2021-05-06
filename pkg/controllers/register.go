package controllers

import (
	"fmt"
	"github.com/loft-sh/jspolicy/pkg/bundle"
	"github.com/loft-sh/jspolicy/pkg/controller"
	"github.com/loft-sh/jspolicy/pkg/util/certhelper"
	"github.com/loft-sh/jspolicy/pkg/util/loghelper"
	"io/ioutil"
	"path/filepath"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// Register registers the webhooks to the manager
func Register(mgr manager.Manager, controllerPolicyManager controller.PolicyManager) error {
	caBundleData, err := ioutil.ReadFile(filepath.Join(certhelper.WebhookCertFolder, "ca.crt"))
	if err != nil {
		return err
	}

	err = (&JsPolicyReconciler{
		Client:   mgr.GetClient(),
		Log:      loghelper.New("jspolicy-controller"),
		Scheme:   mgr.GetScheme(),
		CaBundle: caBundleData,
		Bundler:  bundle.NewJavascriptBundler(),

		ControllerPolicyManager: controllerPolicyManager,

		controllerPolicyHash: map[string]string{},
	}).SetupWithManager(mgr)
	if err != nil {
		return fmt.Errorf("unable to create jspolicy controller: %v", err)
	}

	return nil
}
