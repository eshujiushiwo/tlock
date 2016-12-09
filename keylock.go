package tlock

import (
	"context"
	"fmt"
	"hash/crc32"
	"sort"
	"time"
)

const defaultKeySlotSize = 1024

type KeyLockerGroup struct {
	set []*refLockSet
}

func NewKeyLockerGroup() *KeyLockerGroup {
	g := new(KeyLockerGroup)

	g.set = make([]*refLockSet, defaultKeySlotSize)
	for i := 0; i < defaultKeySlotSize; i++ {
		g.set[i] = newRefLockSet()
	}
	return g

}

func (g *KeyLockerGroup) getSet(key string) *refLockSet {
	index := crc32.ChecksumIEEE([]byte(key)) % uint32(defaultKeySlotSize)
	return g.set[index]
}

// func (g *KeyLockerGroup) Lock(keys ...string) {
// 	// use a very long timeout
// 	b := g.LockTimeout(InfiniteTimeout, keys...)
// 	if !b {
// 		panic("Wait lock too long, panic")
// 	}
// }

func removeDuplicatedItems(keys ...string) []string {
	if len(keys) <= 1 {
		return keys
	}

	m := make(map[string]struct{}, len(keys))

	p := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := m[key]; !ok {
			m[key] = struct{}{}
			p = append(p, key)
		}
	}

	return p
}

func (g *KeyLockerGroup) LockTimeout(ctx context.Context, ch1 chan bool, ch2 chan bool, timeout time.Duration, keys ...string) {
	if len(keys) == 0 {
		panic("empty keys, panic")
	}

	// remove duplicated items
	keys = removeDuplicatedItems(keys...)

	// Sort keys to avoid deadlock
	sort.Strings(keys)

	timer := time.NewTimer(timeout)
	fmt.Println(timeout)
	defer timer.Stop()

	grapNum := 0

	for _, key := range keys {
		//fmt.Println("cccccc", key)
		s := g.getSet(key)
		m := s.Get(key)
		//	b := LockWithTimer(m, timer)
		c1 := make(chan bool, 1)
		c2 := make(chan bool, 1)
		c3 := make(chan bool, 1)

		go LockWithTimer(ctx, m, timer, c1, c2, c3)

		select {
		case <-c1:
			fmt.Println("111")
			grapNum++

		case <-c2:
			fmt.Println("123")
			ch2 <- true
			return

		}

	}

	ch1 <- true
	// go func() {}()
	// select {
	// case <-ctx.Done():
	// 	fmt.Println("yeah")

	// }
}

func (g *KeyLockerGroup) Unlock(keys ...string) {
	if len(keys) == 0 {
		return
	}

	// remove duplicated items
	keys = removeDuplicatedItems(keys...)

	// Reverse Sort keys to avoid deadlock
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))

	for _, key := range keys {
		m := g.getSet(key).RawGet(key)

		if m == nil {
			panic(fmt.Sprintf("%s is not locked, panic", key))
		}

		m.Unlock()

		g.getSet(key).Put(key, m)
	}
}
