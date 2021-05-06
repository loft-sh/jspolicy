package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	"github.com/loft-sh/jspolicy/pkg/metrics"
	"github.com/loft-sh/jspolicy/pkg/util/compress"
	"github.com/loft-sh/jspolicy/pkg/util/loghelper"
	"github.com/loft-sh/jspolicy/pkg/vm/vmpool"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"net/http"
	"os"
	"rogchap.com/v8go"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"strconv"
	"time"
)

type Handler interface {
	Handle(context.Context, admission.Request, *policyv1beta1.JsPolicy) (admission.Response, *Response)
}

func NewHandler(client client.Client, vmPool vmpool.VMPool) Handler {
	return &handler{
		vmPool: vmPool,
		client: client,
		log:    loghelper.New("policy-handler"),
	}
}

type handler struct {
	vmPool vmpool.VMPool
	client client.Client
	log    loghelper.Logger
}

func (h *handler) Handle(ctx context.Context, req admission.Request, jsPolicy *policyv1beta1.JsPolicy) (admission.Response, *Response) {
	// execute policy
	response, rawResponse, elapsed := h.internalHandle(ctx, req, jsPolicy)

	// record metrics
	if jsPolicy != nil {
		statusCode := http.StatusOK
		if response.Result != nil && response.Result.Code != 0 {
			statusCode = int(response.Result.Code)
		}

		policyType := policyv1beta1.PolicyTypeValidating
		if jsPolicy.Spec.Type != "" {
			policyType = jsPolicy.Spec.Type
		}

		metrics.PolicyRequestTotal.WithLabelValues(string(policyType), jsPolicy.Name, strconv.Itoa(statusCode)).Inc()
		if elapsed != 0 {
			metrics.PolicyRequestLatencies.WithLabelValues(string(policyType), jsPolicy.Name).Observe(elapsed.Seconds())
			if os.Getenv("PROFILING") == "true" {
				klog.Infof("Execution of policy %s took %s", jsPolicy.Name, elapsed.String())
			}
		}
	}

	return response, rawResponse
}

func (h *handler) internalHandle(ctx context.Context, req admission.Request, jsPolicy *policyv1beta1.JsPolicy) (admission.Response, *Response, time.Duration) {
	timeout := int32(10)
	if jsPolicy.Spec.TimeoutSeconds != nil {
		timeout = *jsPolicy.Spec.TimeoutSeconds
	}

	// get bundle
	decompressed, err := getBundle(ctx, h.client, jsPolicy)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err), nil, 0
	}

	// execute payload
	responseJson, err, elapsed := runScript(ctx, &req, decompressed, jsPolicy.Name, h.vmPool, time.Duration(timeout)*time.Second)
	if err != nil {
		h.log.Errorf("Error executing policy %s: %v", jsPolicy.Name, err)
		return admission.Errored(http.StatusInternalServerError, err), nil, elapsed
	}

	r := &Response{}
	err = json.Unmarshal([]byte(responseJson), r)
	if err != nil {
		h.log.Errorf("Error unmarshalling policy response %s: %v", jsPolicy.Name, err)
		return admission.Errored(http.StatusInternalServerError, err), nil, elapsed
	}

	// deny
	if r.Deny {
		reason := string(metav1.StatusReasonForbidden)
		if r.Reason != "" {
			reason = r.Reason
		}

		response := admission.Denied(reason)
		response.Result.Message = r.Message
		response.Warnings = r.Warnings
		if r.Code > 0 {
			response.Result.Code = int32(r.Code)
		}
		return response, r, elapsed
	}

	// patched
	if r.Patched != nil && jsPolicy.Spec.Type == policyv1beta1.PolicyTypeMutating {
		patched, err := json.Marshal(r.Patched)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, err), r, elapsed
		}

		original, err := json.Marshal(req.Object)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, err), r, elapsed
		}

		response := admission.PatchResponseFromRaw(original, patched)
		response.Warnings = r.Warnings
		return response, r, elapsed
	}

	response := admission.Allowed("")
	response.Warnings = r.Warnings
	return response, r, elapsed
}

func getBundle(ctx context.Context, client client.Client, jsPolicy *policyv1beta1.JsPolicy) (string, error) {
	// TODO: cache this somehow

	// get bundle
	bundle := &policyv1beta1.JsPolicyBundle{}
	err := client.Get(ctx, types.NamespacedName{Name: jsPolicy.Name}, bundle)
	if err != nil {
		return "", fmt.Errorf("couldn't find javascript bundle for js policy %s", jsPolicy.Name)
	}

	// decompress bundle
	decompressed, err := compress.Decompress(bundle.Spec.Bundle)
	if err != nil {
		return "", fmt.Errorf("error decompressing javascript bundle for js policy %s", jsPolicy.Name)
	}

	return string(decompressed), nil
}

func runScript(ctx context.Context, req *admission.Request, script, origin string, vmPool vmpool.VMPool, timeout time.Duration) (string, error, time.Duration) {
	vm := vmPool.Get(ctx)
	defer func() {
		go func() {
			err := vmPool.Put(vm)
			if err != nil {
				klog.Fatalf("Error recreating VM context: %v", err)
			}
		}()
	}()

	// context deadline exceeded?
	if vm == nil {
		return "", ctx.Err(), 0
	}

	// add the request object to script
	out, err := json.Marshal(req)
	if err != nil {
		return "", errors.Wrap(err, "marshal request object"), 0
	}

	// inject request into script
	script = "var __policy = '" + origin + "'; var request = " + string(out) + "; " + script

	// execute the actual payload & stop time
	now := time.Now()
	_, err = vm.RunScriptWithTimeout(script, origin, timeout)
	elapsed := time.Since(now)
	if err != nil {
		return "", err, elapsed
	}

	// extract response from context
	response, err := vm.Context().Global().Get("__response")
	if err != nil {
		return "", err, elapsed
	}
	responseJson, err := v8go.JSONStringify(vm.Context(), response)
	if err != nil {
		return "", err, elapsed
	}

	return responseJson, nil, elapsed
}

type Response struct {
	Deny     bool                   `json:"deny,omitempty"`
	Reason   string                 `json:"reason,omitempty"`
	Message  string                 `json:"message,omitempty"`
	Code     int                    `json:"code,omitempty"`
	Patched  map[string]interface{} `json:"patched,omitempty"`
	Warnings []string               `json:"warnings,omitempty"`

	// this is only used in background policies
	Reschedule bool `json:"reschedule,omitempty"`
}
