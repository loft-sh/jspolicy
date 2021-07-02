# Contributing to JsPolicy

Thank you for contributing to JsPolicy! Here you can find common questions around developing jsPolicy.

## Developing

We recommend to develop jsPolicy directly in a Kubernetes cluster as it makes feedback a lot quicker. For the quick setup, you'll need to install [devspace](https://github.com/loft-sh/devspace#1-install-devspace), kubectl, helm and make sure you have a local Kubernetes cluster (such as Docker Desktop, minikube, KinD or similar) installed.

Clone the jsPolicy project into a new folder and run:

```
devspace run dev
```

Which should produce an output similar to:

```
[info]   Using namespace 'jspolicy'
[info]   Using kube context 'docker-desktop'
[done] √ Created namespace: jspolicy
[done] √ Created image pull secret jspolicy/devspace-auth-docker           
[info]   Building image 'loftsh/jspolicy:TwKqPQw' with engine 'docker'     
Sending build context to Docker daemon  217.7MB
Step 1/15 : FROM node:16 as builder
16: Pulling from library/node
0bc3020d05f1: Already exists 
...
Step 15/15 : RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} GO111MODULE=on go build -mod vendor -o jspolicy cmd/jspolicy/main.go
 ---> Running in 5160d1e0b498
 ---> 08ea57a93c02
Successfully built 08ea57a93c02
Successfully tagged loftsh/jspolicy:TwKqPQw
[info]   Skip image push for loftsh/jspolicy
[done] √ Done processing image 'loftsh/jspolicy'
[info]   Execute 'helm upgrade jspolicy ./chart --namespace jspolicy --values /var/folders/bc/qxzrp6f93zncnj1xyz25kyp80000gn/T/387972652 --install --kube-context docker-desktop'
[info]   Execute 'helm list --namespace jspolicy --output json --kube-context docker-desktop'
[done] √ Deployed helm chart (Release revision: 1)              
[done] √ Successfully deployed jspolicy with helm               
[done] √ Scaled down Deployment jspolicy/jspolicy                      
[done] √ Successfully replaced pod jspolicy/jspolicy-54b7cf5557-xs54f             
                                                                                  
#########################################################
[info]   DevSpace UI available at: http://localhost:8090
#########################################################

[0:sync] Waiting for pods...
[0:sync] Starting sync...
[0:sync] Sync started on /Users/fabiankramm/Programmieren/go-workspace/src/github.com/loft-sh/jspolicy <-> . (Pod: jspolicy/jspolicy-54b7cf5557-xs54f-devspace)
[0:sync] Waiting for initial sync to complete
[info]   Opening shell to pod:container jspolicy-54b7cf5557-xs54f-devspace:jspolicy
root@jspolicy-54b7cf5557-xs54f-devspace:/workspace# 
```

Then you can start jsPolicy with 
```
go run -mod vendor cmd/jspolicy/main.go
```

Now if you change a file locally, DevSpace will automatically sync the file into the container and you just have to rerun `go run -mod vendor cmd/jspolicy/main.go` within the terminal.

## Tests

You can run the test suite with `hack/test.sh` which should execute all the jsPolicy tests.
