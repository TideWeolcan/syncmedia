package syncplay

// White-box unit tests for RoomManager username allocation and watcher
// removal (defect (d)): username claims must be atomic (allocation +
// reservation), and removing a stale watcher must not evict a newer watcher
// holding the same name.

import (
	"sync"
	"testing"
)

// TestFindFreeUsernameSequentialClaims: two claims for the same base name
// must yield distinct usernames even before any watcher is registered in a
// room — allocation and registration have to be atomic, otherwise two
// connections can be assigned the same name (TOCTOU).
func TestFindFreeUsernameSequentialClaims(t *testing.T) {
	rm := NewRoomManager()
	u1 := rm.findFreeUsername("u")
	u2 := rm.findFreeUsername("u")
	if u1 != "u" {
		t.Errorf("first claim = %q, want u", u1)
	}
	if u2 == u1 {
		t.Errorf("second claim = %q, collides with first claim %q (name not reserved)", u2, u1)
	}

	room := rm.getRoom("unit-seq")
	w1 := newWatcher(u1, nil)
	room.addWatcher(w1)
	w2 := newWatcher(u2, nil)
	room.addWatcher(w2)
	if got := len(room.getWatchers()); got != 2 {
		t.Errorf("room holds %d watchers after registering both claims, want 2 (silent overwrite)", got)
	}
}

// TestFindFreeUsernameConcurrentClaims: concurrent claims for the same base
// name must all yield distinct usernames.
func TestFindFreeUsernameConcurrentClaims(t *testing.T) {
	rm := NewRoomManager()
	const n = 8
	results := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- rm.findFreeUsername("u")
		}()
	}
	wg.Wait()
	close(results)
	seen := make(map[string]bool)
	for name := range results {
		if seen[name] {
			t.Errorf("duplicate username claimed concurrently: %q", name)
		}
		seen[name] = true
	}
	if len(seen) != n {
		t.Errorf("got %d distinct usernames, want %d", len(seen), n)
	}
}

// TestRemoveWatcherKeepsNewerSameNameWatcher: when a newer watcher occupies
// the same name slot, removing the older instance must not evict the newer
// one (removal purely by name deletes the wrong user).
func TestRemoveWatcherKeepsNewerSameNameWatcher(t *testing.T) {
	rm := NewRoomManager()
	room := rm.getRoom("unit-dup")
	w1 := newWatcher("u", nil)
	room.addWatcher(w1)
	w2 := newWatcher("u", nil)
	room.addWatcher(w2) // takes over the "u" slot

	rm.removeWatcher(w1)

	ws := room.getWatchers()
	if len(ws) != 1 || ws[0] != w2 {
		t.Fatalf("room has %d watchers after removing the stale instance, want exactly the newer watcher", len(ws))
	}
	if w2.room != room {
		t.Errorf("newer watcher lost its room reference")
	}
}

// TestFindFreeUsernameSeesRoomWatchers: the allocator must keep consulting
// actual room membership (guarded by room.mu) when picking a free name.
func TestFindFreeUsernameSeesRoomWatchers(t *testing.T) {
	rm := NewRoomManager()
	room := rm.getRoom("unit-room")
	room.addWatcher(newWatcher("taken", nil))
	if got := rm.findFreeUsername("taken"); got != "taken_" {
		t.Errorf("findFreeUsername(taken) = %q, want taken_", got)
	}
}
