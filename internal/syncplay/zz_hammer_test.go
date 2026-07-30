package syncplay

// Concurrent hammer for defect (d), kept in a zz_ file so it runs after all
// other tests in the package. Against the unfixed code it fails
// deterministically on duplicate claims and is very likely to crash the
// runtime with "concurrent map iteration and map write", because
// findFreeUsername iterates room.watchers without holding room.mu while
// addWatcher/removeWatcher mutate the map under room.mu.

import (
	"fmt"
	"sync"
	"testing"
)

func TestZZConcurrentClaimVsRoomMutation(t *testing.T) {
	rm := NewRoomManager()
	room := rm.getRoom("hammer")
	for i := 0; i < 256; i++ {
		room.addWatcher(newWatcher(fmt.Sprintf("seed%03d", i), nil))
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			w := newWatcher(fmt.Sprintf("churn%02d", i%32), nil)
			room.addWatcher(w)
			room.removeWatcher(w)
			i++
		}
	}()

	seen := make(map[string]bool)
	dupReported := false
	for i := 0; i < 400; i++ {
		name := rm.findFreeUsername("user")
		if seen[name] && !dupReported {
			t.Errorf("claim %d returned duplicate username %q (allocation is not atomic)", i, name)
			dupReported = true
		}
		seen[name] = true
	}
	close(stop)
	wg.Wait()
}
