package syncplay

// Bounded-shutdown regression test. Server.Close must return promptly even
// while a live client keeps the connection busy: a client that keeps sending
// legal messages resets the per-read 12.5s deadline every round, so a Close
// that only closes the listener and waits on s.wg blocks without bound.

import (
	"io"
	"net"
	"runtime"
	"testing"
	"time"
)

// minGoroutineCount samples runtime.NumGoroutine several times and returns
// the lowest value, damping transient runtime/test goroutines.
func minGoroutineCount(samples int, pause time.Duration) int {
	lowest := runtime.NumGoroutine()
	for i := 1; i < samples; i++ {
		time.Sleep(pause)
		if n := runtime.NumGoroutine(); n < lowest {
			lowest = n
		}
	}
	return lowest
}

func TestCloseBoundedWithActiveClient(t *testing.T) {
	baseline := minGoroutineCount(5, 10*time.Millisecond)

	env := newTestEnv(t)
	tc := env.dial()
	tc.hello("alice", "rshutdown")

	// Drain server traffic (periodic States, List replies) so server-side
	// writes never back up. Exits when the connection dies.
	go func() { io.Copy(io.Discard, tc.conn) }()

	// Active client: a legal List request every 300ms resets the server's
	// read deadline each round. Exits when the connection dies.
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			tc.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if _, err := tc.conn.Write([]byte(`{"List": {}}` + "\n")); err != nil {
				return
			}
		}
	}()

	// Let at least one keepalive land so the reader is provably active.
	time.Sleep(400 * time.Millisecond)

	closeDone := make(chan struct{})
	go func() {
		env.srv.Close()
		close(closeDone)
	}()

	select {
	case <-closeDone:
	case <-time.After(15 * time.Second):
		t.Fatal("Server.Close did not return within 15s while a client kept sending")
	}

	// The listening port must be released: the same loopback address must be
	// bindable again (brief retry for OS-level lag).
	var (
		relisten net.Listener
		err      error
	)
	for deadline := time.Now().Add(3 * time.Second); ; {
		relisten, err = net.Listen("tcp", env.addr)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("re-listen on %s after Close: %v", env.addr, err)
	}
	relisten.Close()

	// Every connection goroutine (reader, state loop, test helpers) must wind
	// down: poll until the count falls back to baseline plus slack.
	const slack = 2
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= baseline+slack {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines after Close = %d, want <= baseline %d + slack %d", n, baseline, slack)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
