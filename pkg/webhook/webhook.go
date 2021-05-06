package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-logr/logr"
	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	"io"
	"io/ioutil"
	"k8s.io/apimachinery/pkg/types"
	"net/http"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"strings"

	v1 "k8s.io/api/admission/v1"
	"k8s.io/api/admission/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

var admissionScheme = runtime.NewScheme()
var admissionCodecs = serializer.NewCodecFactory(admissionScheme)

func init() {
	utilruntime.Must(v1.AddToScheme(admissionScheme))
	utilruntime.Must(v1beta1.AddToScheme(admissionScheme))
}

type Webhook struct {
	Client  client.Client
	Handler Handler
	Scheme  *runtime.Scheme

	log logr.Logger
}

var _ http.Handler = &Webhook{}

func (wh *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body []byte
	var err error
	var reviewResponse admission.Response
	if r.Body != nil {
		if body, err = ioutil.ReadAll(r.Body); err != nil {
			wh.log.Error(err, "unable to read the body from the incoming request")
			reviewResponse = admission.Errored(http.StatusBadRequest, err)
			wh.writeResponse(w, reviewResponse)
			return
		}
	} else {
		err = errors.New("request body is empty")
		wh.log.Error(err, "bad request")
		reviewResponse = admission.Errored(http.StatusBadRequest, err)
		wh.writeResponse(w, reviewResponse)
		return
	}

	// verify the content type is accurate
	contentType := r.Header.Get("Content-Type")
	if contentType != "application/json" {
		err = fmt.Errorf("contentType=%s, expected application/json", contentType)
		wh.log.Error(err, "unable to process a request with an unknown content type", "content type", contentType)
		reviewResponse = admission.Errored(http.StatusBadRequest, err)
		wh.writeResponse(w, reviewResponse)
		return
	}

	// Both v1 and v1beta1 AdmissionReview types are exactly the same, so the v1beta1 type can
	// be decoded into the v1 type. However the runtime codec's decoder guesses which type to
	// decode into by type name if an Object's TypeMeta isn't set. By setting TypeMeta of an
	// unregistered type to the v1 GVK, the decoder will coerce a v1beta1 AdmissionReview to v1.
	// The actual AdmissionReview GVK will be used to write a typed response in case the
	// webhook config permits multiple versions, otherwise this response will fail.
	req := admission.Request{}
	ar := unversionedAdmissionReview{}
	// avoid an extra copy
	ar.Request = &req.AdmissionRequest
	ar.SetGroupVersionKind(v1.SchemeGroupVersion.WithKind("AdmissionReview"))
	_, actualAdmRevGVK, err := admissionCodecs.UniversalDeserializer().Decode(body, nil, &ar)
	if err != nil {
		wh.log.Error(err, "unable to decode the request")
		reviewResponse = admission.Errored(http.StatusBadRequest, err)
		wh.writeResponse(w, reviewResponse)
		return
	}
	wh.log.V(1).Info("received request", "UID", req.UID, "kind", req.Kind, "resource", req.Resource)

	// find js webhook
	splitted := strings.Split(r.URL.Path, "/")
	if len(splitted) != 3 || splitted[1] != "policy" {
		err = fmt.Errorf("wrong request path: %s", r.URL.Path)
		wh.log.Error(err, "wrong request path")
		reviewResponse = admission.Errored(http.StatusBadRequest, err)
		wh.writeResponse(w, reviewResponse)
		return
	}

	jsPolicyName := splitted[2]
	jsPolicy := &policyv1beta1.JsPolicy{}
	err = wh.Client.Get(r.Context(), types.NamespacedName{Name: jsPolicyName}, jsPolicy)
	if err != nil {
		wh.log.Error(err, "find js policy")
		reviewResponse = admission.Errored(http.StatusBadRequest, err)
		wh.writeResponse(w, reviewResponse)
		return
	}

	// execute the policy
	reviewResponse, _ = wh.Handler.Handle(r.Context(), req, jsPolicy)
	for i, w := range reviewResponse.Warnings {
		reviewResponse.Warnings[i] = fmt.Sprintf("[%s]: %s", jsPolicy.Name, w)
	}

	// check if we need to log response or modify it
	if reviewResponse.Allowed == false {
		if jsPolicy.Spec.AuditPolicy == nil || *jsPolicy.Spec.AuditPolicy != policyv1beta1.AuditPolicySkip {
			go LogRequest(context.TODO(), wh.Client, req, reviewResponse, jsPolicy, wh.Scheme, 5)
		}

		if jsPolicy.Spec.ViolationPolicy != nil {
			if *jsPolicy.Spec.ViolationPolicy == policyv1beta1.ViolationPolicyPolicyDry {
				warnings := reviewResponse.Warnings
				reviewResponse = admission.Allowed("")
				reviewResponse.Warnings = warnings
			} else if *jsPolicy.Spec.ViolationPolicy == policyv1beta1.ViolationPolicyPolicyWarn {
				warnings := reviewResponse.Warnings
				if warnings == nil {
					warnings = []string{}
				}
				if reviewResponse.Result != nil {
					warnings = append(warnings, fmt.Sprintf("[%s]: %s", jsPolicy.Name, reviewResponse.Result.Message))
				}

				reviewResponse = admission.Allowed("")
				reviewResponse.Warnings = warnings
			}
		}
	} else if jsPolicy.Spec.ViolationPolicy != nil && *jsPolicy.Spec.ViolationPolicy == policyv1beta1.ViolationPolicyPolicyDry {
		// Make sure no mutations are coming through
		warnings := reviewResponse.Warnings
		reviewResponse = admission.Allowed("")
		reviewResponse.Warnings = warnings
	}

	// TODO: add panic-recovery for Handle
	if err := reviewResponse.Complete(req); err != nil {
		wh.log.Error(err, "unable to encode response")
		reviewResponse = admission.Errored(http.StatusInternalServerError, err)
		wh.writeResponse(w, reviewResponse)
		return
	}

	wh.writeResponseTyped(w, reviewResponse, actualAdmRevGVK)
}

// writeResponse writes response to w generically, i.e. without encoding GVK information.
func (wh *Webhook) writeResponse(w io.Writer, response admission.Response) {
	wh.writeAdmissionResponse(w, v1.AdmissionReview{Response: &response.AdmissionResponse})
}

// writeResponseTyped writes response to w with GVK set to admRevGVK, which is necessary
// if multiple AdmissionReview versions are permitted by the webhook.
func (wh *Webhook) writeResponseTyped(w io.Writer, response admission.Response, admRevGVK *schema.GroupVersionKind) {
	ar := v1.AdmissionReview{
		Response: &response.AdmissionResponse,
	}
	// Default to a v1 AdmissionReview, otherwise the API server may not recognize the request
	// if multiple AdmissionReview versions are permitted by the webhook config.
	// TODO(estroz): this should be configurable since older API servers won't know about v1.
	if admRevGVK == nil || *admRevGVK == (schema.GroupVersionKind{}) {
		ar.SetGroupVersionKind(v1.SchemeGroupVersion.WithKind("AdmissionReview"))
	} else {
		ar.SetGroupVersionKind(*admRevGVK)
	}
	wh.writeAdmissionResponse(w, ar)
}

// writeAdmissionResponse writes ar to w.
func (wh *Webhook) writeAdmissionResponse(w io.Writer, ar v1.AdmissionReview) {
	err := json.NewEncoder(w).Encode(ar)
	if err != nil {
		wh.log.Error(err, "unable to encode the response")
		wh.writeResponse(w, admission.Errored(http.StatusInternalServerError, err))
	} else {
		res := ar.Response
		if log := wh.log; log.V(1).Enabled() {
			if res.Result != nil {
				log = log.WithValues("code", res.Result.Code, "reason", res.Result.Reason)
			}
			log.V(1).Info("wrote response", "UID", res.UID, "allowed", res.Allowed)
		}
	}
}

// unversionedAdmissionReview is used to decode both v1 and v1beta1 AdmissionReview types.
type unversionedAdmissionReview struct {
	v1.AdmissionReview
}

var _ runtime.Object = &unversionedAdmissionReview{}
