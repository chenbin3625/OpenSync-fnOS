package handler

import (
	"sync"
	"testing"
)

func TestReserveSSEConnectionNeverExceedsGlobalLimit(t *testing.T) {
	globalSSEConns.Store(0)
	defer globalSSEConns.Store(0)
	var wg sync.WaitGroup
	start := make(chan struct{})
	accepted := make(chan bool, maxGlobalSSEConns*2)
	for i := 0; i < maxGlobalSSEConns*2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			accepted <- reserveSSEConnection()
		}()
	}
	close(start)
	wg.Wait()
	close(accepted)
	var count int32
	for ok := range accepted {
		if ok {
			count++
		}
	}
	if count != maxGlobalSSEConns || globalSSEConns.Load() != maxGlobalSSEConns {
		t.Fatalf("accepted=%d active=%d, want %d", count, globalSSEConns.Load(), maxGlobalSSEConns)
	}
	globalSSEConns.Add(-count)
	if !reserveSSEConnection() {
		t.Fatal("released slot cannot be reserved")
	}
}
