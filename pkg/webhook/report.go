package webhook

import (
	"context"
	"log"
	"strconv"
	"time"

	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	policyreportv1alpha2 "github.com/loft-sh/jspolicy/pkg/apis/policyreport/v1alpha2"
	"github.com/loft-sh/jspolicy/pkg/util/clienthelper"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	Source                  = "JsPolicy"
	PolicyReportPrefix      = "js-policy-report-ns-"
	ClusterPolicyReportName = "js-cluster-policy-report"

	CategoryAnnotation = "policy.jspolicy.com/category"
	SeverityAnnotation = "policy.jspolicy.com/severity"
)

func ReportRequest(
	ctx context.Context,
	client client.Client,
	request admission.Request,
	response admission.Response,
	jsPolicy *policyv1beta1.JsPolicy,
	scheme *runtime.Scheme,
	maxEvents int,
	retryCounter int,
) {
	if response.Allowed || jsPolicy == nil || (jsPolicy.Spec.AuditPolicy != nil && *jsPolicy.Spec.AuditPolicy == policyv1beta1.AuditPolicySkip) {
		return
	} else if retryCounter == 0 {
		klog.Errorf("cannot report request for object %s %s as policy report update is still conflicting after several retries", request.Kind.Kind, request.Name)
		return
	}

	if request.Namespace != "" && request.Kind.Kind != "Namespace" {
		handlePolicyReport(ctx, client, request, response, jsPolicy, scheme, maxEvents, retryCounter)
	} else {
		handClusterPolicyReport(ctx, client, request, response, jsPolicy, scheme, maxEvents, retryCounter)
	}
}

func handlePolicyReport(
	ctx context.Context,
	client client.Client,
	request admission.Request,
	response admission.Response,
	jsPolicy *policyv1beta1.JsPolicy,
	scheme *runtime.Scheme,
	maxEvents int,
	retryCounter int,
) {
	// try to get the policy report
	policyReport := &policyreportv1alpha2.PolicyReport{}
	err := client.Get(ctx, types.NamespacedName{Name: PolicyReportPrefix + request.Namespace, Namespace: request.Namespace}, policyReport)
	if err != nil {
		if kerrors.IsNotFound(err) == false {
			klog.Errorf("cannot log request for object %s %s as policy report object cannot be retrieved: %v", request.Kind.Kind, request.Name, err)
			return
		}

		policyReport = &policyreportv1alpha2.PolicyReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      PolicyReportPrefix + request.Namespace,
				Namespace: request.Namespace,
			},
		}
		err := clienthelper.CreateWithOwner(ctx, client, policyReport, jsPolicy, scheme)
		if err != nil {
			if kerrors.IsAlreadyExists(err) == false {
				klog.Errorf("cannot log request for object %s %s as policy report object cannot be created: %v", request.Kind.Kind, request.Name, err)
				return
			}
		}

		ReportRequest(ctx, client, request, response, jsPolicy, scheme, maxEvents, retryCounter)
		return
	}

	// build the violation
	policyresult := buildResult(&request, &response, jsPolicy, resolveAction(jsPolicy))

	// add to status
	if policyReport.Results == nil {
		policyReport.Results = []*policyreportv1alpha2.PolicyReportResult{}
	}

	policyReport.Results = append(policyReport.Results, policyresult)
	if len(policyReport.Results) > maxEvents {
		removed := policyReport.Results[0]
		policyReport.Results = policyReport.Results[1:]

		switch removed.Result {
		case policyreportv1alpha2.StatusFail:
			policyReport.Summary.Fail -= 1
		case policyreportv1alpha2.StatusError:
			policyReport.Summary.Error -= 1
		case policyreportv1alpha2.StatusWarn:
			policyReport.Summary.Warn -= 1
		case policyreportv1alpha2.StatusPass:
			policyReport.Summary.Pass -= 1
		case policyreportv1alpha2.StatusSkip:
			policyReport.Summary.Skip -= 1
		}
	}

	switch policyresult.Result {
	case policyreportv1alpha2.StatusFail:
		policyReport.Summary.Fail += 1
	case policyreportv1alpha2.StatusError:
		policyReport.Summary.Error += 1
	case policyreportv1alpha2.StatusWarn:
		policyReport.Summary.Warn += 1
	case policyreportv1alpha2.StatusPass:
		policyReport.Summary.Pass += 1
	case policyreportv1alpha2.StatusSkip:
		policyReport.Summary.Skip += 1
	}

	updateOrRetry(ctx, policyReport, client, request, response, jsPolicy, scheme, maxEvents, retryCounter)
}

func handClusterPolicyReport(
	ctx context.Context,
	client client.Client,
	request admission.Request,
	response admission.Response,
	jsPolicy *policyv1beta1.JsPolicy,
	scheme *runtime.Scheme,
	maxEvents int,
	retryCounter int,
) {
	// try to get the cluster policy report
	policyReport := &policyreportv1alpha2.ClusterPolicyReport{}
	err := client.Get(ctx, types.NamespacedName{Name: ClusterPolicyReportName}, policyReport)
	if err != nil {
		if kerrors.IsNotFound(err) == false {
			klog.Errorf("cannot log request for object %s %s as policy report object cannot be retrieved: %v", request.Kind.Kind, request.Name, err)
			return
		}

		policyReport = &policyreportv1alpha2.ClusterPolicyReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: ClusterPolicyReportName,
			},
		}
		err := clienthelper.CreateWithOwner(ctx, client, policyReport, jsPolicy, scheme)
		if err != nil {
			if kerrors.IsAlreadyExists(err) == false {
				klog.Errorf("cannot log request for object %s %s as policy report object cannot be created: %v", request.Kind.Kind, request.Name, err)
				return
			}
		}

		ReportRequest(ctx, client, request, response, jsPolicy, scheme, maxEvents, retryCounter)
		return
	}

	// build the violation
	policyresult := buildResult(&request, &response, jsPolicy, resolveAction(jsPolicy))

	// add to status
	if policyReport.Results == nil {
		policyReport.Results = []*policyreportv1alpha2.PolicyReportResult{}
	}

	policyReport.Results = append(policyReport.Results, policyresult)

	if len(policyReport.Results) > maxEvents {
		removed := policyReport.Results[0]
		policyReport.Results = policyReport.Results[1:]

		switch removed.Result {
		case policyreportv1alpha2.StatusFail:
			policyReport.Summary.Fail -= 1
		case policyreportv1alpha2.StatusError:
			policyReport.Summary.Error -= 1
		case policyreportv1alpha2.StatusWarn:
			policyReport.Summary.Warn -= 1
		case policyreportv1alpha2.StatusPass:
			policyReport.Summary.Pass -= 1
		case policyreportv1alpha2.StatusSkip:
			policyReport.Summary.Skip -= 1
		}
	}

	switch policyresult.Result {
	case policyreportv1alpha2.StatusFail:
		policyReport.Summary.Fail += 1
	case policyreportv1alpha2.StatusError:
		policyReport.Summary.Error += 1
	case policyreportv1alpha2.StatusWarn:
		policyReport.Summary.Warn += 1
	case policyreportv1alpha2.StatusPass:
		policyReport.Summary.Pass += 1
	case policyreportv1alpha2.StatusSkip:
		policyReport.Summary.Skip += 1
	}

	updateOrRetry(ctx, policyReport, client, request, response, jsPolicy, scheme, maxEvents, retryCounter)
}

func updateOrRetry(
	ctx context.Context,
	object client.Object,
	client client.Client,
	request admission.Request,
	response admission.Response,
	jsPolicy *policyv1beta1.JsPolicy,
	scheme *runtime.Scheme,
	maxEvents int,
	retryCounter int,
) {
	// try to update object
	err := client.Update(ctx, object)
	if err != nil {
		if kerrors.IsNotFound(err) {
			return
		} else if kerrors.IsConflict(err) {
			log.Printf("ERROR %s", err.Error())
			ReportRequest(ctx, client, request, response, jsPolicy, scheme, maxEvents, retryCounter-1)
			return
		}

		klog.Errorf("cannot log request for object %s %s: %v", request.Kind.Kind, request.Name, err)
		return
	}
}

func resolveAction(jsPolicy *policyv1beta1.JsPolicy) policyv1beta1.ViolationPolicyType {
	action := policyv1beta1.ViolationPolicyPolicyDeny
	if jsPolicy.Spec.ViolationPolicy != nil {
		action = *jsPolicy.Spec.ViolationPolicy
	}
	if jsPolicy.Spec.Type == policyv1beta1.PolicyTypeController {
		action = policyv1beta1.ViolationPolicyPolicyController
	}

	return action
}

func mapStatus(result policyv1beta1.ViolationPolicyType) policyreportv1alpha2.PolicyResult {
	switch result {
	case policyv1beta1.ViolationPolicyPolicyDeny:
		return policyreportv1alpha2.StatusFail
	case policyv1beta1.ViolationPolicyPolicyWarn:
		return policyreportv1alpha2.StatusWarn
	case policyv1beta1.ViolationPolicyPolicyController:
		return policyreportv1alpha2.StatusPass
	}

	return policyreportv1alpha2.StatusSkip
}

func mapSeverity(severity string) policyreportv1alpha2.PolicySeverity {
	switch severity {
	case policyreportv1alpha2.SeverityLow:
		return policyreportv1alpha2.SeverityLow
	case policyreportv1alpha2.SeverityMedium:
		return policyreportv1alpha2.SeverityMedium
	case policyreportv1alpha2.SeverityHigh:
		return policyreportv1alpha2.SeverityHigh
	}

	return policyreportv1alpha2.SeverityHigh
}

func buildResult(request *admission.Request, response *admission.Response, jsPolicy *policyv1beta1.JsPolicy, result policyv1beta1.ViolationPolicyType) *policyreportv1alpha2.PolicyReportResult {
	policyresult := &policyreportv1alpha2.PolicyReportResult{
		Result: mapStatus(result),
		Source: Source,
		Resources: []*corev1.ObjectReference{
			{
				Kind:       request.Kind.Kind,
				Namespace:  request.Namespace,
				Name:       request.Name,
				APIVersion: request.Kind.Version,
				UID:        request.UID,
			},
		},
		Policy:    jsPolicy.Name,
		Timestamp: metav1.Timestamp{Seconds: time.Now().Unix()},
	}

	if category, ok := jsPolicy.Annotations[CategoryAnnotation]; ok {
		policyresult.Category = category
	}

	if severity, ok := jsPolicy.Annotations[SeverityAnnotation]; ok {
		policyresult.Severity = mapSeverity(severity)
	}

	if response.Result != nil {
		message := response.Result.Message

		// make sure message is not huge
		if len(message) > 256 {
			message = message[0:253] + "..."
		}

		policyresult.Message = message
		policyresult.Properties = map[string]string{
			"operation":         string(request.Operation),
			"reason":            string(response.Result.Reason),
			"code":              strconv.FormatInt(int64(response.Result.Code), 10),
			"userinfo.username": request.UserInfo.Username,
		}

		if request.UserInfo.UID != "" {
			policyresult.Properties["userinfo.uuid"] = request.UserInfo.UID
		}
	}

	return policyresult
}
