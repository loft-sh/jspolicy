#!/bin/bash

set -e

echo "Generate deepcopy & client ..."

deepcopy-gen --input-dirs github.com/loft-sh/jspolicy/pkg/apis/... -o $GOPATH/src --go-header-file ./hack/boilerplate.go.txt -O zz_generated.deepcopy
client-gen -o $GOPATH/src --go-header-file ./hack/boilerplate.go.txt --input-base github.com/loft-sh/jspolicy/pkg/apis --input policy/v1beta1,policyreport/v1alpha2 --clientset-path github.com/loft-sh/jspolicy/pkg/client/clientset_generated --clientset-name clientset

echo "Generate crd ..."
go run gen/main.go > chart/crds/crds.yaml
