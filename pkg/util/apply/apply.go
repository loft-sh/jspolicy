package apply

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	klog "k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	applyAnnotation = "jspolicy.com/apply"
)

func Apply(ctx context.Context, c client.Client, obj client.Object, force bool) error {
	obj = obj.DeepCopyObject().(client.Object)
	err := ensureType(obj, c.Scheme())
	if err != nil {
		return err
	}

	if len(obj.GetName()) == 0 {
		generatedName := obj.GetGenerateName()
		if len(generatedName) > 0 {
			return fmt.Errorf("from %s: cannot use generate name with apply", generatedName)
		}
	} else if obj.GetResourceVersion() != "" {
		return fmt.Errorf("shouldn't use apply with already existing resources")
	}

	// Get the modified configuration of the object. Embed the result
	// as an annotation in the modified configuration, so that it will appear
	// in the patch sent to the server.
	modified, err := GetModifiedConfiguration(obj, true, unstructured.UnstructuredJSONScheme)
	if err != nil {
		return fmt.Errorf("retrieving modified configuration from:\n%s\nfor: %v", obj.GetName(), err)
	}

	serverObj, err := NewObjectFromExisting(obj, c.Scheme())
	if err != nil {
		return err
	}

	err = c.Get(ctx, types.NamespacedName{
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}, serverObj)
	if err != nil {
		if !kerrors.IsNotFound(err) {
			return fmt.Errorf("retrieving current configuration of:\n%s\nfrom server: %v", obj.GetName(), err)
		}

		// Create the resource if it doesn't exist
		// First, update the annotation used by kubectl apply
		if err := CreateApplyAnnotation(obj, unstructured.UnstructuredJSONScheme); err != nil {
			return errors.Wrap(err, "creating")
		}

		typeAccessor, _ := meta.TypeAccessor(obj)
		klog.Infof("Create object %s %s %s", typeAccessor.GetKind(), typeAccessor.GetAPIVersion(), obj.GetName())

		// Then create the resource and skip the three-way merge
		err = c.Create(ctx, obj)
		if err != nil {
			return errors.Wrap(err, "creating")
		}

		return nil
	}
	err = ensureType(serverObj, c.Scheme())
	if err != nil {
		return err
	}

	patcher, err := newPatcher(c, force)
	if err != nil {
		return err
	}

	patchBytes, _, err := patcher.Patch(ctx, serverObj, modified)
	if err != nil {
		return fmt.Errorf("applying patch:\n%s\nerror:\n%v\n", string(patchBytes), err)
	}

	return nil
}

func GVKFrom(obj runtime.Object, scheme *runtime.Scheme) (schema.GroupVersionKind, error) {
	gvks, _, err := scheme.ObjectKinds(obj)
	if err != nil {
		return schema.GroupVersionKind{}, err
	} else if len(gvks) != 1 {
		return schema.GroupVersionKind{}, fmt.Errorf("unexpected number of object kinds: %d", len(gvks))
	}

	return gvks[0], nil
}

func ensureType(obj client.Object, scheme *runtime.Scheme) error {
	gvk, err := GVKFrom(obj, scheme)
	if err != nil {
		return err
	}

	typeAccessor, err := meta.TypeAccessor(obj)
	if err != nil {
		return err
	}

	typeAccessor.SetKind(gvk.Kind)
	typeAccessor.SetAPIVersion(gvk.GroupVersion().String())
	return nil
}

func NewObjectFromExisting(newObj client.Object, scheme *runtime.Scheme) (client.Object, error) {
	gvk, err := GVKFrom(newObj, scheme)
	if err != nil {
		return nil, err
	}

	obj, err := scheme.New(gvk)
	if err != nil {
		if runtime.IsNotRegisteredError(err) {
			u, ok := newObj.(*unstructured.Unstructured)
			if ok {
				obj := &unstructured.Unstructured{}
				obj.SetKind(u.GetKind())
				obj.SetAPIVersion(u.GetAPIVersion())
				return obj, nil
			}
		}

		return nil, err
	}

	typeAccessor, err := meta.TypeAccessor(obj)
	if err != nil {
		return nil, err
	}

	typeAccessor.SetKind(gvk.Kind)
	typeAccessor.SetAPIVersion(gvk.GroupVersion().String())
	return obj.(client.Object), nil
}
