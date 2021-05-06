package webhook

import (
	"context"
	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	"github.com/loft-sh/jspolicy/pkg/util/clienthelper"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const DefaultAuditLogSize = 20

func LogRequest(ctx context.Context, client client.Client, request admission.Request, response admission.Response, jsPolicy *policyv1beta1.JsPolicy, scheme *runtime.Scheme, retryCounter int) {
	if response.Allowed || jsPolicy == nil || (jsPolicy.Spec.AuditPolicy != nil && *jsPolicy.Spec.AuditPolicy == policyv1beta1.AuditPolicySkip) {
		return
	} else if retryCounter == 0 {
		klog.Errorf("cannot log request for object %s %s as js policy violations status update is still conflicting after several retries", request.Kind.Kind, request.Name)
		return
	}

	// try to get the js policy violations
	jsPolicyViolations := &policyv1beta1.JsPolicyViolations{}
	err := client.Get(ctx, types.NamespacedName{Name: jsPolicy.Name}, jsPolicyViolations)
	if err != nil {
		if kerrors.IsNotFound(err) == false {
			klog.Errorf("cannot log request for object %s %s as js policy violations object cannot be retrieved: %v", request.Kind.Kind, request.Name, err)
			return
		}

		jsPolicyViolations = &policyv1beta1.JsPolicyViolations{
			ObjectMeta: metav1.ObjectMeta{
				Name: jsPolicy.Name,
			},
		}
		err := clienthelper.CreateWithOwner(ctx, client, jsPolicyViolations, jsPolicy, scheme)
		if err != nil {
			if kerrors.IsAlreadyExists(err) == false {
				klog.Errorf("cannot log request for object %s %s as js policy violations object cannot be created: %v", request.Kind.Kind, request.Name, err)
				return
			}
		}

		LogRequest(ctx, client, request, response, jsPolicy, scheme, retryCounter)
		return
	}

	action := string(policyv1beta1.ViolationPolicyPolicyDeny)
	if jsPolicy.Spec.ViolationPolicy != nil {
		action = string(*jsPolicy.Spec.ViolationPolicy)
	}
	if jsPolicy.Spec.Type == policyv1beta1.PolicyTypeController {
		action = string(policyv1beta1.ViolationPolicyPolicyController)
	}

	// build the violation
	violation := buildViolation(&request, &response, action)

	// check if we need to delete entries
	logSize := DefaultAuditLogSize
	if jsPolicy.Spec.AuditLogSize != nil && *jsPolicy.Spec.AuditLogSize >= 0 && *jsPolicy.Spec.AuditLogSize < 40 {
		logSize = int(*jsPolicy.Spec.AuditLogSize)
	}

	// add to status
	if jsPolicyViolations.Status.Violations == nil {
		jsPolicyViolations.Status.Violations = []policyv1beta1.PolicyViolation{}
	}
	jsPolicyViolations.Status.Violations = append(jsPolicyViolations.Status.Violations, *violation)

	// trim if log exceeds size
	if len(jsPolicyViolations.Status.Violations) > logSize {
		jsPolicyViolations.Status.Violations = jsPolicyViolations.Status.Violations[len(jsPolicyViolations.Status.Violations)-logSize : len(jsPolicyViolations.Status.Violations)]
	}

	// try to update object
	err = client.Status().Update(ctx, jsPolicyViolations)
	if err != nil {
		if kerrors.IsNotFound(err) {
			return
		} else if kerrors.IsConflict(err) {
			LogRequest(ctx, client, request, response, jsPolicy, scheme, retryCounter)
			return
		}

		klog.Errorf("cannot log request for object %s %s: %v", request.Kind.Kind, request.Name, err)
	}
}

func buildViolation(request *admission.Request, response *admission.Response, action string) *policyv1beta1.PolicyViolation {
	violation := &policyv1beta1.PolicyViolation{
		Action: action,
		RequestInfo: &policyv1beta1.RequestInfo{
			Kind:      request.Kind.Kind,
			Namespace: request.Namespace,
			Name:      request.Name,
			Operation: request.Operation,
			APIVersion: schema.GroupVersion{
				Group:   request.Kind.Group,
				Version: request.Kind.Version,
			}.String(),
		},
		UserInfo: &policyv1beta1.UserInfo{
			Username: request.UserInfo.Username,
			UID:      request.UserInfo.UID,
		},
		Timestamp: metav1.Now(),
	}

	if response.Result != nil {
		message := response.Result.Message

		// make sure message is not huge
		if len(message) > 256 {
			message = message[0:253] + "..."
		}

		violation.Message = message
		violation.Reason = string(response.Result.Reason)
		violation.Code = response.Result.Code
	}

	return violation
}
