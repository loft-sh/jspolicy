package vmpool

import (
	"context"
	"fmt"
	vm2 "github.com/loft-sh/jspolicy/pkg/vm"
	"gotest.tools/assert"
	"math/rand"
	"rogchap.com/v8go"
	"sync"
	"testing"
	"time"
)

func TestVMPoolSimple(t *testing.T) {
	// create a new vm pool
	vmPool, err := NewVMPool(10, func() (vm2.VM, error) {
		return &fakeVM{}, nil
	})
	assert.NilError(t, err)

	// set random seed
	rand.Seed(time.Now().UnixNano())

	// now try to get the vms
	waitGroup := sync.WaitGroup{}
	errors := make(chan error, 100)
	for i := 0; i < 100; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()

			vm := vmPool.Get(context.Background())
			if vm == nil {
				errors <- fmt.Errorf("vm is nil")
			} else {
				time.Sleep(time.Millisecond * time.Duration(rand.Intn(10)))
			}

			_ = vmPool.Put(vm)
		}()
	}

	waitGroup.Wait()
	if len(errors) > 0 {
		t.Fatal(<-errors)
	}
}

func TestVMPoolTimeout(t *testing.T) {
	// create a new vm pool
	vmPool, err := NewVMPool(5, func() (vm2.VM, error) {
		return &fakeVM{}, nil
	})
	assert.NilError(t, err)

	// set random seed
	rand.Seed(time.Now().UnixNano())

	// now try to get the vms
	waitGroup := sync.WaitGroup{}
	errors := make(chan error, 100)
	timeoutCtx, cancel := context.WithTimeout(context.Background(), time.Millisecond*200)
	defer cancel()
	for i := 0; i < 100; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()

			vm := vmPool.Get(timeoutCtx)
			if vm == nil {
				vm = vmPool.Get(context.Background())
				if vm == nil {
					errors <- fmt.Errorf("vm is nil")
				}
			} else {
				time.Sleep(time.Millisecond * 100 * time.Duration(rand.Intn(10)))
			}

			_ = vmPool.Put(vm)
		}()
	}

	waitGroup.Wait()
	if len(errors) > 0 {
		t.Fatal(<-errors)
	}
}

type fakeVM struct{}

func (f *fakeVM) Context() *v8go.Context { return nil }
func (f *fakeVM) RunScriptWithTimeout(script string, origin string, timeout time.Duration) (*v8go.Value, error) {
	return nil, nil
}
func (f *fakeVM) RunScriptSafe(script string, origin string) (val *v8go.Value, err error) {
	return nil, nil
}
func (f *fakeVM) RecreateContext() error { return nil }
