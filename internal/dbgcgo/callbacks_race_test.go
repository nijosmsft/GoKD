package dbgcgo

// Regression test for the send-on-closed-channel race between the C
// event callback trampoline (goEventCallback) and UnregisterCallbacks.
//
// Drives the registry through the non-cgo test helpers in callbacks.go
// so this _test.go file can build (cgo is not allowed in _test files).

import (
	"sync"
	"testing"
	"time"
)

func TestEventChannelCloseRace(t *testing.T) {
	const iterations = 200
	for i := 0; i < iterations; i++ {
		handle := uint64(0xDEAD0000) + uint64(i)
		ch := make(chan Event, 8)
		registerEventChanForTest(handle, ch)

		// Drain in the background so the send isn't always-dropped.
		var drained sync.WaitGroup
		drained.Add(1)
		go func() {
			defer drained.Done()
			for range ch {
			}
		}()

		var producers sync.WaitGroup
		producers.Add(1)
		go func() {
			defer producers.Done()
			for j := 0; j < 2000; j++ {
				fireBreakpointEventForTest(handle)
			}
		}()

		time.Sleep(10 * time.Microsecond)
		unregisterEventChanForTest(handle)

		producers.Wait()
		drained.Wait()
	}
}
