package webhook

import (
	"github.com/loft-sh/jspolicy/pkg/util/encoding"
	"github.com/loft-sh/jspolicy/pkg/vm/vmpool"
	"github.com/loft-sh/jspolicy/pkg/webhook/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

func Register(mgr ctrl.Manager, vmPool vmpool.VMPool, enablePolicyReports bool, policyReportMaxEvents int) error {
	genericWebhook := &Webhook{
		Client:  mgr.GetClient(),
		Handler: NewHandler(mgr.GetClient(), vmPool),
		Scheme:  mgr.GetScheme(),

		enablePolicyReports:   enablePolicyReports,
		policyReportMaxEvents: policyReportMaxEvents,
		log:                   ctrl.Log.WithName("webhook"),
	}

	webhookServer := mgr.GetWebhookServer()
	webhookServer.Register("/policy/", genericWebhook)
	webhookServer.Register("/crds", &webhook.Admission{Handler: &validation.Validator{
		Log:           ctrl.Log.WithName("webhooks").WithName("Validator"),
		StrictDecoder: encoding.NewDecoder(mgr.GetScheme(), true),
		NormalDecoder: encoding.NewDecoder(mgr.GetScheme(), false),
	}})
	return nil
}
