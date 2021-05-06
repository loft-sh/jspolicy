package vmpool

import (
	"context"
	vm2 "github.com/loft-sh/jspolicy/pkg/vm"
	"golang.org/x/sync/semaphore"
	"sync"
)

type VMPool interface {
	Get(ctx context.Context) vm2.VM
	Put(vm vm2.VM) error
}

type vmpool struct {
	stack     *stack
	semaphore *semaphore.Weighted
}

func NewVMPool(size int, newVM func() (vm2.VM, error)) (VMPool, error) {
	stack := &stack{}
	for i := 0; i < size; i++ {
		iso, err := newVM()
		if err != nil {
			return nil, err
		}

		stack.Push(iso)
	}

	return &vmpool{
		stack:     stack,
		semaphore: semaphore.NewWeighted(int64(size)),
	}, nil
}

func (v *vmpool) Get(ctx context.Context) vm2.VM {
	err := v.semaphore.Acquire(ctx, 1)
	if err != nil {
		return nil
	}

	ret := v.stack.Pop()
	if ret == nil {
		panic("returned object from the stack was nil")
	}

	return ret.(vm2.VM)
}

func (v *vmpool) Put(vm vm2.VM) error {
	if vm == nil {
		return nil
	}

	// we recreate the context here
	err := vm.RecreateContext()
	if err != nil {
		return err
	}

	v.stack.Push(vm)
	v.semaphore.Release(1)
	return nil
}

type stack struct {
	lock sync.Mutex
	data []vm2.VM
}

func (s *stack) Push(v vm2.VM) {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.data = append(s.data, v)
}

func (s *stack) Pop() vm2.VM {
	s.lock.Lock()
	defer s.lock.Unlock()

	if len(s.data) == 0 {
		return nil
	}

	l := len(s.data)
	obj := s.data[l-1]
	s.data = s.data[:l-1]
	return obj
}
