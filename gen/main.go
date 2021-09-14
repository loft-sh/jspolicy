package main

import (
	"fmt"
	"log"

	"github.com/ghodss/yaml"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-tools/pkg/crd"
	crdmarkers "sigs.k8s.io/controller-tools/pkg/crd/markers"
	"sigs.k8s.io/controller-tools/pkg/loader"
	"sigs.k8s.io/controller-tools/pkg/markers"
)

func main() {
	pkgs, err := loader.LoadRoots("./pkg/apis/policy/v1beta1", "./pkg/apis/policyreport/v1alpha2")
	if err != nil {
		log.Fatal(err)
	}

	reg := &markers.Registry{}
	err = crdmarkers.Register(reg)
	if err != nil {
		log.Fatal(err)
	}
	parser := &crd.Parser{
		Collector: &markers.Collector{Registry: reg},
		Checker:   &loader.TypeChecker{},
	}
	policyPkg := pkgs[0]
	policyreportPkg := pkgs[1]
	outputCRD(parser, policyPkg, "JsPolicy", "policy.jspolicy.com", apiextensionsv1.ClusterScoped)
	fmt.Println("---")
	outputCRD(parser, policyPkg, "JsPolicyViolations", "policy.jspolicy.com", apiextensionsv1.ClusterScoped)
	fmt.Println("---")
	outputCRD(parser, policyPkg, "JsPolicyBundle", "policy.jspolicy.com", apiextensionsv1.ClusterScoped)
	fmt.Println("---")
	outputCRD(parser, policyreportPkg, "PolicyReport", "wgpolicyk8s.io", apiextensionsv1.NamespaceScoped)
	fmt.Println("---")
	outputCRD(parser, policyreportPkg, "ClusterPolicyReport", "wgpolicyk8s.io", apiextensionsv1.ClusterScoped)
}

func outputCRD(parser *crd.Parser, v1Pkg *loader.Package, kind, group string, scope apiextensionsv1.ResourceScope) {
	crd.AddKnownTypes(parser)

	parser.NeedPackage(v1Pkg)

	groupKind := schema.GroupKind{Kind: kind, Group: group}
	parser.NeedCRDFor(groupKind, nil)
	crd, ok := parser.CustomResourceDefinitions[groupKind]
	if ok {
		crd.Spec.Scope = scope
		out, err := yaml.Marshal(crd)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Println(string(out))
	} else {
		log.Fatal("Not found")
	}
}
