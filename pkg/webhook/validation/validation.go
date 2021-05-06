package validation

import (
	"context"
	"fmt"
	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	"github.com/loft-sh/jspolicy/pkg/util/encoding"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/go-logr/logr"

	"k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type Validator struct {
	Log           logr.Logger
	StrictDecoder encoding.Decoder
	NormalDecoder encoding.Decoder
}

func (v *Validator) Handle(ctx context.Context, req admission.Request) admission.Response {
	var (
		obj    runtime.Object
		oldObj runtime.Object
		err    error
		kind   = schema.GroupVersionKind{
			Group:   req.Kind.Group,
			Version: req.Kind.Version,
			Kind:    req.Kind.Kind,
		}
	)

	if req.Operation == v1.Create {
		obj, err = v.StrictDecoder.Decode(req.Object.Raw)
		if err != nil {
			response := admission.Denied(string(metav1.StatusReasonBadRequest))
			response.Result.Message = err.Error()
			return response
		}
	} else if req.Operation == v1.Update {
		obj, err = v.StrictDecoder.Decode(req.Object.Raw)
		if err != nil {
			response := admission.Denied(string(metav1.StatusReasonBadRequest))
			response.Result.Message = err.Error()
			return response
		}

		oldObj, err = v.NormalDecoder.Decode(req.OldObject.Raw)
		if err != nil {
			return admission.Errored(1, err)
		}
	} else {
		return admission.Errored(1, fmt.Errorf("operation %s not supported", string(req.Operation)))
	}

	// We allow other api groups
	if kind.GroupVersion().String() != policyv1beta1.SchemeGroupVersion.String() {
		return admission.Allowed("")
	}

	if req.Operation != v1.Create && req.Operation != v1.Update {
		return admission.Errored(1, fmt.Errorf("operation %s not supported", string(req.Operation)))
	}

	var errs field.ErrorList
	switch kind.Kind {
	case "JsPolicy":
		if req.Operation == v1.Create {
			errs = ValidateJsPolicy(obj.(*policyv1beta1.JsPolicy))
		} else {
			errs = ValidateJsPolicyUpdate(obj.(*policyv1beta1.JsPolicy), oldObj.(*policyv1beta1.JsPolicy))
		}
	}
	if len(errs) > 0 {
		response := admission.Denied(string(metav1.StatusReasonForbidden))
		response.Result.Message = errs.ToAggregate().Error()
		return response
	}

	return admission.Allowed("")
}
