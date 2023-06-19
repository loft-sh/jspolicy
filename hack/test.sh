#!/usr/bin/env bash

# Set required go flags
# export GO111MODULE=on
# export GOFLAGS=-mod=vendor

# Test if we can build the program
echo "Building js policy..."
go build cmd/jspolicy/main.go || exit 1

# List packages
PKGS=$(go list ./... | grep -v /examples/ | grep -v /test/)

echo "Start testing..."
fail=false
for pkg in $PKGS; do
 go test -race -coverprofile=profile.out -covermode=atomic $pkg
 if [ $? -ne 0 ]; then
   fail=true
 fi
done

if [ "$fail" = true ]; then
 echo "Failure"
 exit 1
fi
