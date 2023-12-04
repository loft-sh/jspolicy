/*
Copyright 2017 The Kubernetes Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package validation

import (
	"fmt"
	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func hasWildcard(slice []string) bool {
	for _, s := range slice {
		if s == "*" {
			return true
		}
	}
	return false
}

func validateResources(resources []string, fldPath *field.Path) field.ErrorList {
	var allErrors field.ErrorList
	if len(resources) == 0 {
		allErrors = append(allErrors, field.Required(fldPath, ""))
	}

	// */x
	resourcesWithWildcardSubresoures := sets.String{}
	// x/*
	subResourcesWithWildcardResource := sets.String{}
	// */*
	hasDoubleWildcard := false
	// *
	hasSingleWildcard := false
	// x
	hasResourceWithoutSubresource := false

	for i, resSub := range resources {
		if resSub == "" {
			allErrors = append(allErrors, field.Required(fldPath.Index(i), ""))
			continue
		}
		if resSub == "*/*" {
			hasDoubleWildcard = true
		}
		if resSub == "*" {
			hasSingleWildcard = true
		}
		parts := strings.SplitN(resSub, "/", 2)
		if len(parts) == 1 {
			hasResourceWithoutSubresource = resSub != "*"
			continue
		}
		res, sub := parts[0], parts[1]
		if _, ok := resourcesWithWildcardSubresoures[res]; ok {
			allErrors = append(allErrors, field.Invalid(fldPath.Index(i), resSub, fmt.Sprintf("if '%s/*' is present, must not specify %s", res, resSub)))
		}
		if _, ok := subResourcesWithWildcardResource[sub]; ok {
			allErrors = append(allErrors, field.Invalid(fldPath.Index(i), resSub, fmt.Sprintf("if '*/%s' is present, must not specify %s", sub, resSub)))
		}
		if sub == "*" {
			resourcesWithWildcardSubresoures[res] = struct{}{}
		}
		if res == "*" {
			subResourcesWithWildcardResource[sub] = struct{}{}
		}
	}
	if len(resources) > 1 && hasDoubleWildcard {
		allErrors = append(allErrors, field.Invalid(fldPath, resources, "if '*/*' is present, must not specify other resources"))
	}
	if hasSingleWildcard && hasResourceWithoutSubresource {
		allErrors = append(allErrors, field.Invalid(fldPath, resources, "if '*' is present, must not specify other resources without subresources"))
	}
	return allErrors
}

func validateResourcesNoSubResources(resources []string, fldPath *field.Path) field.ErrorList {
	var allErrors field.ErrorList
	if len(resources) == 0 {
		allErrors = append(allErrors, field.Required(fldPath, ""))
	}
	for i, resource := range resources {
		if resource == "" {
			allErrors = append(allErrors, field.Required(fldPath.Index(i), ""))
		}
		if strings.Contains(resource, "/") {
			allErrors = append(allErrors, field.Invalid(fldPath.Index(i), resource, "must not specify subresources"))
		}
	}
	if len(resources) > 1 && hasWildcard(resources) {
		allErrors = append(allErrors, field.Invalid(fldPath, resources, "if '*' is present, must not specify other resources"))
	}
	return allErrors
}

var validScopes = sets.NewString(
	string(admissionregistrationv1.ClusterScope),
	string(admissionregistrationv1.NamespacedScope),
	string(admissionregistrationv1.AllScopes),
)

func validateRule(rule *policyv1beta1.JsPolicySpec, fldPath *field.Path, allowSubResource bool) field.ErrorList {
	var allErrors field.ErrorList
	if len(rule.APIGroups) > 1 && hasWildcard(rule.APIGroups) {
		allErrors = append(allErrors, field.Invalid(fldPath.Child("apiGroups"), rule.APIGroups, "if '*' is present, must not specify other API groups"))
	}
	// Note: group could be empty, e.g., the legacy "v1" API
	if len(rule.APIVersions) > 1 && hasWildcard(rule.APIVersions) {
		allErrors = append(allErrors, field.Invalid(fldPath.Child("apiVersions"), rule.APIVersions, "if '*' is present, must not specify other API versions"))
	}
	for i, version := range rule.APIVersions {
		if version == "" {
			allErrors = append(allErrors, field.Required(fldPath.Child("apiVersions").Index(i), ""))
		}
	}
	if allowSubResource {
		allErrors = append(allErrors, validateResources(rule.Resources, fldPath.Child("resources"))...)
	} else {
		allErrors = append(allErrors, validateResourcesNoSubResources(rule.Resources, fldPath.Child("resources"))...)
	}
	if rule.Scope != nil && !validScopes.Has(string(*rule.Scope)) {
		allErrors = append(allErrors, field.NotSupported(fldPath.Child("scope"), *rule.Scope, validScopes.List()))
	}
	return allErrors
}

// ValidateJsPolicy validates a webhook before creation.
func ValidateJsPolicy(e *policyv1beta1.JsPolicy) field.ErrorList {
	return validateValidatingWebhook(e.Name, &e.Spec, nil, field.NewPath("spec"))
}

func validateValidatingWebhook(name string, hook *policyv1beta1.JsPolicySpec, oldHook *policyv1beta1.JsPolicySpec, fldPath *field.Path) field.ErrorList {
	var allErrors field.ErrorList
	// hook.Name must be fully qualified
	allErrors = append(allErrors, utilvalidation.IsFullyQualifiedName(field.NewPath("metadata", "name"), name)...)

	allErrors = append(allErrors, validateRuleWithOperations(hook, fldPath.Child("rules"))...)
	if oldHook != nil && hook.Type != oldHook.Type {
		allErrors = append(allErrors, field.Invalid(fldPath.Child("type"), hook.Type, "type is immutable"))
	}
	if hook.FailurePolicy != nil && !supportedFailurePolicies.Has(string(*hook.FailurePolicy)) {
		allErrors = append(allErrors, field.NotSupported(fldPath.Child("failurePolicy"), *hook.FailurePolicy, supportedFailurePolicies.List()))
	}
	if hook.ReinvocationPolicy != nil && !supportedReinvocationPolicies.Has(string(*hook.ReinvocationPolicy)) {
		allErrors = append(allErrors, field.NotSupported(fldPath.Child("reinvocationPolicy"), *hook.ReinvocationPolicy, supportedReinvocationPolicies.List()))
	}
	if hook.MatchPolicy != nil && !supportedMatchPolicies.Has(string(*hook.MatchPolicy)) {
		allErrors = append(allErrors, field.NotSupported(fldPath.Child("matchPolicy"), *hook.MatchPolicy, supportedMatchPolicies.List()))
	}
	if hook.Type != "" && !supportedPolicyTypes.Has(string(hook.Type)) {
		allErrors = append(allErrors, field.NotSupported(fldPath.Child("type"), hook.Type, supportedPolicyTypes.List()))
	}
	if hook.TimeoutSeconds != nil && (*hook.TimeoutSeconds > 30 || *hook.TimeoutSeconds < 1) {
		allErrors = append(allErrors, field.Invalid(fldPath.Child("timeoutSeconds"), *hook.TimeoutSeconds, "the timeout value must be between 1 and 30 seconds"))
	}
	if hook.NamespaceSelector != nil {
		allErrors = append(allErrors, ValidateLabelSelector(hook.NamespaceSelector, fldPath.Child("namespaceSelector"))...)
	}

	if hook.ObjectSelector != nil {
		allErrors = append(allErrors, ValidateLabelSelector(hook.ObjectSelector, fldPath.Child("objectSelector"))...)
	}
	if hook.AuditPolicy != nil {
		allErrors = append(allErrors, validateAuditPolicy(hook.AuditPolicy, fldPath.Child("auditPolicy"))...)
	}
	if hook.ViolationPolicy != nil {
		allErrors = append(allErrors, validateViolationPolicy(hook.ViolationPolicy, fldPath.Child("violationPolicy"))...)
	}
	if hook.AuditLogSize != nil {
		allErrors = append(allErrors, validateAuditLogSize(hook.AuditLogSize, fldPath.Child("auditLogSize"))...)
	}

	return allErrors
}

func validateAuditLogSize(size *int32, path *field.Path) field.ErrorList {
	if size == nil {
		return nil
	}
	if *size <= 0 || *size > 40 {
		return field.ErrorList{field.Invalid(path, *size, "The audit log size needs to be between 1 and 40")}
	}

	return nil
}

func validateAuditPolicy(policy *policyv1beta1.AuditPolicyType, path *field.Path) field.ErrorList {
	if policy == nil {
		return nil
	}
	if *policy != policyv1beta1.AuditPolicyLog && *policy != policyv1beta1.AuditPolicySkip {
		return field.ErrorList{field.Invalid(path, *policy, fmt.Sprintf("needs to be one of %v", []policyv1beta1.AuditPolicyType{policyv1beta1.AuditPolicyLog, policyv1beta1.AuditPolicySkip}))}
	}

	return nil
}

func validateViolationPolicy(policy *policyv1beta1.ViolationPolicyType, path *field.Path) field.ErrorList {
	if policy == nil {
		return nil
	}
	if *policy != policyv1beta1.ViolationPolicyPolicyDeny && *policy != policyv1beta1.ViolationPolicyPolicyWarn && *policy != policyv1beta1.ViolationPolicyPolicyDry {
		return field.ErrorList{field.Invalid(path, *policy, fmt.Sprintf("needs to be one of %v", []policyv1beta1.ViolationPolicyType{policyv1beta1.ViolationPolicyPolicyDeny, policyv1beta1.ViolationPolicyPolicyWarn, policyv1beta1.ViolationPolicyPolicyDry}))}
	}

	return nil
}

var supportedFailurePolicies = sets.NewString(
	string(admissionregistrationv1.Ignore),
	string(admissionregistrationv1.Fail),
)

var supportedPolicyTypes = sets.NewString(
	string(""),
	string(policyv1beta1.PolicyTypeMutating),
	string(policyv1beta1.PolicyTypeValidating),
	string(policyv1beta1.PolicyTypeController),
)

var supportedMatchPolicies = sets.NewString(
	string(admissionregistrationv1.Exact),
	string(admissionregistrationv1.Equivalent),
)

var noSideEffectClasses = sets.NewString(
	string(admissionregistrationv1.SideEffectClassNone),
	string(admissionregistrationv1.SideEffectClassNoneOnDryRun),
)

var supportedOperations = sets.NewString(
	string(admissionregistrationv1.OperationAll),
	string(admissionregistrationv1.Create),
	string(admissionregistrationv1.Delete),
	string(admissionregistrationv1.Update),
	string(admissionregistrationv1.Connect),
)

var supportedReinvocationPolicies = sets.NewString(
	string(admissionregistrationv1.NeverReinvocationPolicy),
	string(admissionregistrationv1.IfNeededReinvocationPolicy),
)

func hasWildcardOperation(operations []admissionregistrationv1.OperationType) bool {
	for _, o := range operations {
		if o == admissionregistrationv1.OperationAll {
			return true
		}
	}
	return false
}

func validateRuleWithOperations(ruleWithOperations *policyv1beta1.JsPolicySpec, fldPath *field.Path) field.ErrorList {
	var allErrors field.ErrorList
	if len(ruleWithOperations.Operations) == 0 {
		allErrors = append(allErrors, field.Required(fldPath.Child("operations"), ""))
	}
	if len(ruleWithOperations.Operations) > 1 && hasWildcardOperation(ruleWithOperations.Operations) {
		allErrors = append(allErrors, field.Invalid(fldPath.Child("operations"), ruleWithOperations.Operations, "if '*' is present, must not specify other operations"))
	}
	for i, operation := range ruleWithOperations.Operations {
		if !supportedOperations.Has(string(operation)) {
			allErrors = append(allErrors, field.NotSupported(fldPath.Child("operations").Index(i), operation, supportedOperations.List()))
		}
		if ruleWithOperations.Type == policyv1beta1.PolicyTypeController && (operation == admissionregistrationv1.Update || operation == admissionregistrationv1.Connect) {
			allErrors = append(allErrors, field.NotSupported(fldPath.Child("operations").Index(i), operation, []string{
				string(admissionregistrationv1.OperationAll),
				string(admissionregistrationv1.Create),
				string(admissionregistrationv1.Delete),
			}))
		}
	}
	allowSubResource := true
	allErrors = append(allErrors, validateRule(ruleWithOperations, fldPath, allowSubResource)...)
	return allErrors
}

// ValidateJsPolicyUpdate validates update of validating webhook configuration
func ValidateJsPolicyUpdate(newC, oldC *policyv1beta1.JsPolicy) field.ErrorList {
	return validateValidatingWebhook(newC.Name, &newC.Spec, &oldC.Spec, field.NewPath("spec"))
}

func ValidateLabelSelector(ps *metav1.LabelSelector, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if ps == nil {
		return allErrs
	}
	allErrs = append(allErrs, ValidateLabels(ps.MatchLabels, fldPath.Child("matchLabels"))...)
	for i, expr := range ps.MatchExpressions {
		allErrs = append(allErrs, ValidateLabelSelectorRequirement(expr, fldPath.Child("matchExpressions").Index(i))...)
	}
	return allErrs
}

func ValidateLabelSelectorRequirement(sr metav1.LabelSelectorRequirement, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	switch sr.Operator {
	case metav1.LabelSelectorOpIn, metav1.LabelSelectorOpNotIn:
		if len(sr.Values) == 0 {
			allErrs = append(allErrs, field.Required(fldPath.Child("values"), "must be specified when `operator` is 'In' or 'NotIn'"))
		}
	case metav1.LabelSelectorOpExists, metav1.LabelSelectorOpDoesNotExist:
		if len(sr.Values) > 0 {
			allErrs = append(allErrs, field.Forbidden(fldPath.Child("values"), "may not be specified when `operator` is 'Exists' or 'DoesNotExist'"))
		}
	default:
		allErrs = append(allErrs, field.Invalid(fldPath.Child("operator"), sr.Operator, "not a valid selector operator"))
	}
	allErrs = append(allErrs, ValidateLabelName(sr.Key, fldPath.Child("key"))...)
	return allErrs
}

// ValidateLabelName validates that the label name is correctly defined.
func ValidateLabelName(labelName string, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	for _, msg := range validation.IsQualifiedName(labelName) {
		allErrs = append(allErrs, field.Invalid(fldPath, labelName, msg))
	}
	return allErrs
}

// ValidateLabels validates that a set of labels are correctly defined.
func ValidateLabels(labels map[string]string, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	for k, v := range labels {
		allErrs = append(allErrs, ValidateLabelName(k, fldPath)...)
		for _, msg := range validation.IsValidLabelValue(v) {
			allErrs = append(allErrs, field.Invalid(fldPath, v, msg))
		}
	}
	return allErrs
}
