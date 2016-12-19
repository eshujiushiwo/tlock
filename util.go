package tlock

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// func LockTimeout(m sync.Locker, timeout time.Duration) bool {
// 	timer := time.NewTimer(timeout)
// 	defer timer.Stop()
// 	return LockWithTimer(m, timer)
// }

func LockWithTimer(ctx context.Context, m sync.Locker, timer *time.Timer, c1 chan bool, c2 chan bool) {
	done := make(chan bool, 1)
	decided := new(int32)
	go func() {

		m.Lock()

		done <- true

	}()

	select {

	case <-ctx.Done():
		if atomic.SwapInt32(decided, 1) != 1 {
			fmt.Println("acer")
			c2 <- true
		}
		return

	case <-done:
		c1 <- true

	}

}
