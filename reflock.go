package tlock

import (
	"fmt"
	"sync"
)

type refLock struct {
	sync.RWMutex
	ref     int
	workers []*worker
}
type worker struct {
	name   string
	source chan interface{}
}
type refLockSet struct {
	sync.Mutex
	set     map[string]*refLock
	workers []*worker
}

func newRefLockSet() *refLockSet {
	s := new(refLockSet)

	s.set = make(map[string]*refLock, 16)

	return s
}

func (s *refLockSet) Get(key string) *refLock {
	s.Lock()
	defer s.Unlock()

	v, ok := s.set[key]
	if ok {
		v.ref++

		s.workers = append(s.workers, &worker{})
	} else {
		v = &refLock{ref: 1}

		s.set[key] = v
	}

	return v
}

func (s *refLockSet) RawGet(key string) *refLock {
	s.Lock()
	defer s.Unlock()

	v := s.set[key]
	return v
}

func (s *refLockSet) Put(key string, v *refLock) {
	s.Lock()
	defer s.Unlock()

	v.ref--
	fmt.Println("jjjj", len(s.workers))
	if v.ref <= 0 {

		delete(s.set, key)
	}
}
