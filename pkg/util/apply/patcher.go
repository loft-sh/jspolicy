/*
Copyright 2019 The Kubernetes Authors.
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

package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pkg/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	klog "k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jonboulle/clockwork"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	// maxPatchRetry is the maximum number of conflicts retry for during a patch operation before returning failure
	maxPatchRetry = 5
	// backOffPeriod is the period to back off when apply patch results in error.
	backOffPeriod = 1 * time.Second
	// how many times we can retry before back off
	triesBeforeBackOff = 1
)

// Patcher defines options to patch OpenAPI objects.
type Patcher struct {
	Client    client.Client
	Overwrite bool
	BackOff   clockwork.Clock

	Force   bool
	Timeout time.Duration

	// Number of retries to make if the patch fails with conflict
	Retries int
}

func newPatcher(client client.Client, force bool) (*Patcher, error) {
	return &Patcher{
		Client:    client,
		Overwrite: true,
		BackOff:   clockwork.NewRealClock(),
		Force:     force,
		Timeout:   time.Second * 10,
		Retries:   maxPatchRetry,
	}, nil
}

func (p *Patcher) patchSimple(ctx context.Context, obj client.Object, modified []byte) ([]byte, runtime.Object, error) {
	// Serialize the current configuration of the object from the server.
	current, err := runtime.Encode(unstructured.UnstructuredJSONScheme, obj)
	if err != nil {
		return nil, nil, fmt.Errorf("serializing current configuration from:\n%v\nerr:%v", obj, err)
	}

	// Retrieve the original configuration of the object from the annotation.
	original, err := GetOriginalConfiguration(obj)
	if err != nil {
		return nil, nil, fmt.Errorf("retrieving original configuration from:\n%v\nerr:%v", obj, err)
	}

	var patchType types.PatchType
	var patch []byte
	var lookupPatchMeta strategicpatch.LookupPatchMeta
	createPatchErrFormat := "creating patch with:\noriginal:\n%s\nmodified:\n%s\ncurrent:\n%s\nerr:%v"

	// Create the versioned struct from the type defined in the restmapping
	// (which is the API version we'll be submitting the patch to)
	gvk, err := GVKFrom(obj, p.Client.Scheme())
	if err != nil {
		return nil, nil, err
	}

	versionedObject, err := p.Client.Scheme().New(gvk)
	switch {
	case runtime.IsNotRegisteredError(err):
		// fall back to generic JSON merge patch
		patchType = types.MergePatchType
		patch, err = jsonmergepatch.CreateThreeWayJSONMergePatch(original, modified, current)
		if err != nil {
			return nil, nil, fmt.Errorf(createPatchErrFormat, original, modified, current, err)
		}
	case err != nil:
		return nil, nil, fmt.Errorf("getting instance of versioned object for %v: %v", obj, err)
	case err == nil:
		// Compute a three way strategic merge patch to send to server.
		patchType = types.MergePatchType

		if patch == nil {
			lookupPatchMeta, err = strategicpatch.NewPatchMetaFromStruct(versionedObject)
			if err != nil {
				return nil, nil, fmt.Errorf(createPatchErrFormat, original, modified, current, err)
			}
			patch, err = strategicpatch.CreateThreeWayMergePatch(original, modified, current, lookupPatchMeta, p.Overwrite)
			if err != nil {
				return nil, nil, fmt.Errorf(createPatchErrFormat, original, modified, current, err)
			}
		}
	}

	// strip patch of unnecessary fields
	patch, err = p.stripPatch(patch)
	if err != nil {
		return nil, nil, errors.Wrap(err, "strip patch")
	} else if len(patch) == 0 || string(patch) == "{}" {
		return patch, obj, nil
	}

	if obj.GetNamespace() != "" {
		klog.Infof("Apply patch on %s %s %s/%s: %s", gvk.Kind, gvk.GroupVersion().String(), obj.GetNamespace(), obj.GetName(), string(patch))
	} else {
		klog.Infof("Apply patch on %s %s %s: %s", gvk.Kind, gvk.GroupVersion().String(), obj.GetName(), string(patch))
	}

	patchedObj := obj.DeepCopyObject()
	err = p.Client.Patch(ctx, patchedObj.(client.Object), client.RawPatch(patchType, patch))
	if err != nil {
		return nil, nil, err
	}

	return patch, patchedObj, nil
}

func (p *Patcher) stripPatch(patch []byte) ([]byte, error) {
	patchObj := map[string]interface{}{}
	err := json.Unmarshal(patch, &patchObj)
	if err != nil {
		return nil, err
	}

	// cleanup metadata
	allowedFields := map[string]bool{
		"labels":          true,
		"annotations":     true,
		"ownerReferences": true,
		"finalizers":      true,
	}
	if patchObj["metadata"] != nil {
		metadataObj, ok := patchObj["metadata"].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("unexpected metadata format: %#+v", patchObj["metadata"])
		}

		for k := range metadataObj {
			if !allowedFields[k] {
				delete(metadataObj, k)
			}
		}
		if len(metadataObj) == 0 {
			delete(patchObj, "metadata")
		} else if len(patchObj) == 1 && len(metadataObj) == 1 && metadataObj["annotations"] != nil {
			// check if only value is patch annotation
			annoObj, ok := metadataObj["annotations"].(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("unexpected annotations format: %#+v", metadataObj["annotations"])
			} else if len(annoObj) == 1 && annoObj[applyAnnotation] != nil {
				return nil, nil
			}
		}
	}

	// only patch if we have at least a single item to patch
	if len(patchObj) == 0 {
		return nil, nil
	}
	return json.Marshal(patchObj)
}

// Patch tries to patch an OpenAPI resource. On success, returns the merge patch as well
// the final patched object. On failure, returns an error.
func (p *Patcher) Patch(ctx context.Context, current client.Object, modified []byte) ([]byte, runtime.Object, error) {
	namespace := current.GetNamespace()
	name := current.GetName()

	var (
		getErr error
		newErr error
	)
	patchBytes, patchObject, err := p.patchSimple(ctx, current, modified)
	if p.Retries == 0 {
		p.Retries = maxPatchRetry
	}
	for i := 1; i <= p.Retries && kerrors.IsConflict(err); i++ {
		if i > triesBeforeBackOff {
			p.BackOff.Sleep(backOffPeriod)
		}

		current, newErr = NewObjectFromExisting(current, p.Client.Scheme())
		if newErr != nil {
			return nil, nil, newErr
		}

		getErr = p.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, current)
		if getErr != nil {
			return nil, nil, getErr
		}
		patchBytes, patchObject, err = p.patchSimple(ctx, current, modified)
	}
	if err != nil && (kerrors.IsConflict(err) || kerrors.IsInvalid(err)) && p.Force {
		patchBytes, patchObject, err = p.deleteAndCreate(ctx, current, modified, namespace, name)
	}
	return patchBytes, patchObject, err
}

func (p *Patcher) deleteAndCreate(ctx context.Context, original client.Object, modified []byte, namespace, name string) ([]byte, runtime.Object, error) {
	if err := p.Client.Delete(ctx, original.DeepCopyObject().(client.Object)); err != nil {
		return modified, nil, err
	}
	// TODO: use wait
	if err := wait.PollImmediate(1*time.Second, p.Timeout, func() (bool, error) {
		if err := p.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, original.DeepCopyObject().(client.Object)); !kerrors.IsNotFound(err) {
			return false, err
		}
		return true, nil
	}); err != nil {
		return modified, nil, err
	}
	versionedObjectRaw, _, err := unstructured.UnstructuredJSONScheme.Decode(modified, nil, nil)
	if err != nil {
		return modified, nil, err
	}

	versionedObject, ok := versionedObjectRaw.(client.Object)
	if !ok {
		return modified, nil, fmt.Errorf("unexpected versioned object")
	}

	versionedObject.SetName(name)
	versionedObject.SetNamespace(namespace)
	err = p.Client.Create(ctx, versionedObject)
	if err != nil {
		return nil, nil, err
	}
	return modified, versionedObject, err
}
