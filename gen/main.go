package main

import (
	"fmt"
	"github.com/ghodss/yaml"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"log"
	"sigs.k8s.io/controller-tools/pkg/crd"
	crdmarkers "sigs.k8s.io/controller-tools/pkg/crd/markers"
	"sigs.k8s.io/controller-tools/pkg/loader"
	"sigs.k8s.io/controller-tools/pkg/markers"
)

func main() {
	pkgs, err := loader.LoadRoots("./pkg/apis/policy/v1beta1")
	if err != nil {
		log.Fatal(err)
	}

	// find the virtual cluster package
	v1Pkg := pkgs[0]
	reg := &markers.Registry{}
	err = crdmarkers.Register(reg)
	if err != nil {
		log.Fatal(err)
	}

	parser := &crd.Parser{
		Collector: &markers.Collector{Registry: reg},
		Checker:   &loader.TypeChecker{},
	}
	crd.AddKnownTypes(parser)

	parser.NeedPackage(v1Pkg)

	groupKind := schema.GroupKind{Kind: "JsPolicy", Group: "policy.jspolicy.com"}
	parser.NeedCRDFor(groupKind, nil)
	crd, ok := parser.CustomResourceDefinitions[groupKind]
	if ok {
		crd.Spec.Scope = apiextensionsv1.ClusterScoped
		out, err := yaml.Marshal(crd)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Println(string(out))
	} else {
		log.Fatal("Not found")
	}

	groupKind = schema.GroupKind{Kind: "JsPolicyViolations", Group: "policy.jspolicy.com"}
	parser.NeedCRDFor(groupKind, nil)
	crd, ok = parser.CustomResourceDefinitions[groupKind]
	if ok {
		crd.Spec.Scope = apiextensionsv1.ClusterScoped
		out, err := yaml.Marshal(crd)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Println("---")
		fmt.Println(string(out))
	} else {
		log.Fatal("Not found")
	}

	groupKind = schema.GroupKind{Kind: "JsPolicyBundle", Group: "policy.jspolicy.com"}
	parser.NeedCRDFor(groupKind, nil)
	crd, ok = parser.CustomResourceDefinitions[groupKind]
	if ok {
		crd.Spec.Scope = apiextensionsv1.ClusterScoped
		out, err := yaml.Marshal(crd)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Println("---")
		fmt.Println(string(out))
	} else {
		log.Fatal("Not found")
	}
}
