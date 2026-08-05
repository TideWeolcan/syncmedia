package syncplay

// Black-box protocol tests: they drive a real Server through loopback TCP
// connections speaking the syncplay wire protocol (one JSON object per line,
// '\n' terminated). The tests live in package syncplay only to reach the
// unexported listener field, so they can bind 127.0.0.1:0 instead of the
// production ":port" wildcard bind.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"testing"
	"time"
)

// testEnv owns a running Server plus every client connection opened against
// it. Teardown closes the client connections first, then Server.Close, which
// itself force-closes any still-registered connection before waiting for the
// reader goroutines.
type testEnv struct {
	t     *testing.T
	srv   *Server
	addr  string
	conns []net.Conn
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	srv := NewServer(ServerConfig{Logger: log.New(io.Discard, "", 0)})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	srv.listener = ln
	go srv.Serve()
	env := &testEnv{t: t, srv: srv, addr: ln.Addr().String()}
	t.Cleanup(env.teardown)
	return env
}

func (e *testEnv) teardown() {
	for _, c := range e.conns {
		_ = c.Close()
	}
	_ = e.srv.Close()
}

func (e *testEnv) dial() *testClient {
	e.t.Helper()
	conn, err := net.Dial("tcp", e.addr)
	if err != nil {
		e.t.Fatalf("dial %s: %v", e.addr, err)
	}
	e.conns = append(e.conns, conn)
	return &testClient{t: e.t, conn: conn, rd: bufio.NewReader(conn)}
}

type testClient struct {
	t    *testing.T
	conn net.Conn
	rd   *bufio.Reader
	name string
}

func nowUnix() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

func (tc *testClient) sendRaw(line string) {
	tc.t.Helper()
	tc.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := tc.conn.Write([]byte(line + "\n")); err != nil {
		tc.t.Fatalf("write %q: %v", line, err)
	}
}

// readMessage reads one JSON line (skipping blank lines) before deadline.
func (tc *testClient) readMessage(deadline time.Time) (*Message, error) {
	for {
		if err := tc.conn.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
		line, err := tc.rd.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		line = trimSpace(line)
		if len(line) == 0 {
			continue
		}
		var m Message
		if err := json.Unmarshal(line, &m); err != nil {
			return nil, fmt.Errorf("decode %q: %w", line, err)
		}
		return &m, nil
	}
}

// waitFor keeps reading server messages — tolerating interleaved periodic
// State broadcasts, join/ready notices, etc. — until pred matches.
func (tc *testClient) waitFor(desc string, timeout time.Duration, pred func(*Message) bool) *Message {
	tc.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		m, err := tc.readMessage(deadline)
		if err != nil {
			tc.t.Fatalf("waiting for %s: %v", desc, err)
		}
		if pred(m) {
			return m
		}
	}
}

// hello performs the Hello handshake and records the server-assigned name.
func (tc *testClient) hello(username, room string) *HelloMsg {
	tc.t.Helper()
	tc.sendRaw(fmt.Sprintf(`{"Hello":{"username":%q,"room":{"name":%q},"version":"1.7.0"}}`, username, room))
	m := tc.waitFor("Hello response", 3*time.Second, func(m *Message) bool { return m.Hello != nil })
	tc.name = m.Hello.Username
	return m.Hello
}

// sendPlaystate reports a client playstate. latencyCalculation is the current
// unix time so the server computes ~zero RTT/forward delay and position
// assertions stay exact (tests also keep rooms paused for the same reason).
func (tc *testClient) sendPlaystate(pos float64, paused, doSeek bool) {
	tc.t.Helper()
	tc.sendRaw(fmt.Sprintf(
		`{"State":{"ping":{"latencyCalculation":%.6f,"clientRtt":0},"playstate":{"position":%g,"paused":%t,"doSeek":%t}}}`,
		nowUnix(), pos, paused, doSeek))
}

// ackServerIgnore acknowledges a forced State (ignoringOnTheFly.server).
func (tc *testClient) ackServerIgnore(n int) {
	tc.t.Helper()
	tc.sendRaw(fmt.Sprintf(
		`{"State":{"ignoringOnTheFly":{"server":%d},"ping":{"latencyCalculation":%.6f,"clientRtt":0}}}`,
		n, nowUnix()))
}

// waitForForcedState waits for a forced State broadcast, identified by
// ignoringOnTheFly.server > 0 (periodic States never carry it).
func (tc *testClient) waitForForcedState(desc string, timeout time.Duration) *StateMsg {
	tc.t.Helper()
	m := tc.waitFor(desc, timeout, func(m *Message) bool {
		return m.State != nil && m.State.IgnoringOnTheFly != nil && m.State.IgnoringOnTheFly.Server > 0
	})
	return m.State
}

// listRoomUsers issues a List request ({"List": {}} — null would be ignored)
// and returns the sorted usernames present in room.
func (tc *testClient) listRoomUsers(room string) []string {
	tc.t.Helper()
	tc.sendRaw(`{"List": {}}`)
	m := tc.waitFor("List response", 3*time.Second, func(m *Message) bool { return m.List != nil })
	return roomUsersFromList(m, room)
}

func roomUsersFromList(m *Message, room string) []string {
	rooms, ok := m.List.(map[string]any)
	if !ok {
		return nil
	}
	users, ok := rooms[room].(map[string]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(users))
	for name := range users {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// pollRoomUsers polls List until the room contains exactly want (sorted) or
// the deadline passes; it returns the last observed user list either way.
func (tc *testClient) pollRoomUsers(room string, want []string, timeout time.Duration) []string {
	tc.t.Helper()
	deadline := time.Now().Add(timeout)
	var last []string
	for {
		last = tc.listRoomUsers(room)
		if equalStrings(last, want) || time.Now().After(deadline) {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func isLeftEvent(m *Message) bool {
	if m.Set == nil || m.Set.User == nil {
		return false
	}
	for _, ev := range m.Set.User {
		if ev != nil && ev.Event != nil && ev.Event.Left {
			return true
		}
	}
	return false
}

func keysOfUserEvents(m map[string]*UserEvent) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// TestHelloHandshake covers the join handshake: the server must assign the
// username, echo the client version, and include realversion/motd/features.
func TestHelloHandshake(t *testing.T) {
	env := newTestEnv(t)
	c := env.dial()
	c.sendRaw(`{"Hello":{"username":"alice","room":{"name":"r1"},"version":"1.7.0"}}`)

	deadline := time.Now().Add(3 * time.Second)
	var root map[string]any
	for {
		c.conn.SetReadDeadline(deadline)
		line, err := c.rd.ReadBytes('\n')
		if err != nil {
			t.Fatalf("reading Hello response: %v", err)
		}
		line = trimSpace(line)
		if len(line) == 0 {
			continue
		}
		root = nil
		if err := json.Unmarshal(line, &root); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		if _, ok := root["Hello"]; ok {
			break
		}
	}

	h, ok := root["Hello"].(map[string]any)
	if !ok {
		t.Fatalf("Hello payload is %T, want object", root["Hello"])
	}
	if got := h["username"]; got != "alice" {
		t.Errorf("username = %v, want alice", got)
	}
	room, _ := h["room"].(map[string]any)
	if got := room["name"]; got != "r1" {
		t.Errorf("room.name = %v, want r1", got)
	}
	if got := h["version"]; got != "1.7.0" {
		t.Errorf("version echo = %v, want 1.7.0", got)
	}
	if got := h["realversion"]; got != ServerVersion {
		t.Errorf("realversion = %v, want %s", got, ServerVersion)
	}
	if _, ok := h["motd"]; !ok {
		t.Errorf("motd field missing from Hello response")
	}
	feats, _ := h["features"].(map[string]any)
	if feats == nil {
		t.Fatalf("features missing from Hello response")
	}
	if got := feats["chat"]; got != true {
		t.Errorf("features.chat = %v, want true", got)
	}
	if got := feats["maxChatMessageLength"]; got != float64(MaxChatLength) {
		t.Errorf("features.maxChatMessageLength = %v, want %d", got, MaxChatLength)
	}
	if got := feats["maxUsernameLength"]; got != float64(MaxUsernameLength) {
		t.Errorf("features.maxUsernameLength = %v, want %d", got, MaxUsernameLength)
	}
}

// TestPlayPauseBroadcast covers unpause and pause propagation to the other
// client via forced State broadcasts.
func TestPlayPauseBroadcast(t *testing.T) {
	env := newTestEnv(t)
	a := env.dial()
	a.hello("alice", "rp")
	b := env.dial()
	b.hello("bob", "rp")

	// alice presses play at position 10 (nonzero: keep clear of defect (a)).
	a.sendPlaystate(10, false, false)
	st := b.waitForForcedState("forced play State", 3*time.Second)
	if st.Playstate == nil {
		t.Fatalf("forced play State has no playstate")
	}
	if st.Playstate.Paused {
		t.Errorf("after play: paused = true, want false")
	}
	if st.Playstate.SetBy != "alice" {
		t.Errorf("after play: setBy = %q, want alice", st.Playstate.SetBy)
	}
	if d := st.Playstate.Position - 10; d < -2 || d > 2 {
		t.Errorf("after play: position = %v, want ~10", st.Playstate.Position)
	}
	b.ackServerIgnore(st.IgnoringOnTheFly.Server)

	// alice must ack her own copy of the forced State, otherwise the server
	// ignores her next playstate.
	sa := a.waitForForcedState("own forced play State", 3*time.Second)
	a.ackServerIgnore(sa.IgnoringOnTheFly.Server)

	// alice pauses at position 11.
	a.sendPlaystate(11, true, false)
	st = b.waitForForcedState("forced pause State", 3*time.Second)
	if st.Playstate == nil {
		t.Fatalf("forced pause State has no playstate")
	}
	if !st.Playstate.Paused {
		t.Errorf("after pause: paused = false, want true")
	}
	if d := st.Playstate.Position - 11; d < -2 || d > 2 {
		t.Errorf("after pause: position = %v, want ~11", st.Playstate.Position)
	}
}

// TestNonzeroSeekBroadcast is the control for defect (a): a seek to a nonzero
// position must reach the other client with the seek target.
func TestNonzeroSeekBroadcast(t *testing.T) {
	env := newTestEnv(t)
	a := env.dial()
	a.hello("alice", "rs")
	b := env.dial()
	b.hello("bob", "rs")

	a.sendPlaystate(50, true, true)
	st := b.waitForForcedState("forced seek State", 3*time.Second)
	if st.Playstate == nil || !st.Playstate.DoSeek {
		t.Fatalf("expected doSeek State, got %+v", st)
	}
	if !st.Playstate.Paused {
		t.Errorf("seek broadcast paused = false, want true")
	}
	if d := st.Playstate.Position - 50; d < -1 || d > 1 {
		t.Errorf("seek position = %v, want ~50", st.Playstate.Position)
	}
}

// TestSeekToZeroReachesRoom exercises defect (a): a legitimate seek back to
// position 0 must be broadcast as position 0, not swallowed by a
// "position != 0" sentinel so that the stale pre-seek position leaks out.
func TestSeekToZeroReachesRoom(t *testing.T) {
	env := newTestEnv(t)
	a := env.dial()
	a.hello("alice", "rz")
	b := env.dial()
	b.hello("bob", "rz")

	// Establish a nonzero watcher position first (no seek, no broadcast).
	a.sendPlaystate(100, true, false)
	// Then seek back to the very beginning.
	a.sendPlaystate(0, true, true)

	st := b.waitForForcedState("forced seek-to-0 State", 3*time.Second)
	if st.Playstate == nil || !st.Playstate.DoSeek {
		t.Fatalf("expected doSeek State, got %+v", st)
	}
	if pos := st.Playstate.Position; pos > 1 || pos < -1 {
		t.Errorf("seek-to-0 broadcast position = %v, want ~0 (stale pre-seek position leaked)", pos)
	}
}

// TestForcedSeekPersistsInPeriodicState exercises defect (b): after a seek is
// broadcast and acked, the next periodic State must still carry the seek
// target — the room's authoritative position — and not snap back to the
// stale pre-seek position.
func TestForcedSeekPersistsInPeriodicState(t *testing.T) {
	env := newTestEnv(t)
	a := env.dial()
	a.hello("alice", "rb")
	b := env.dial()
	b.hello("bob", "rb")

	a.sendPlaystate(500, true, true)

	st := b.waitForForcedState("forced seek State", 3*time.Second)
	if st.Playstate == nil || !st.Playstate.DoSeek {
		t.Fatalf("expected doSeek State, got %+v", st)
	}
	if d := st.Playstate.Position - 500; d < -1 || d > 1 {
		t.Fatalf("forced seek position = %v, want ~500", st.Playstate.Position)
	}
	b.ackServerIgnore(st.IgnoringOnTheFly.Server)

	// Next periodic State: the paused room must still be at ~500.
	next := b.waitFor("periodic State after ack", 3*time.Second, func(m *Message) bool {
		return m.State != nil && m.State.Playstate != nil && !m.State.Playstate.DoSeek
	})
	if d := next.State.Playstate.Position - 500; d < -1 || d > 1 {
		t.Errorf("periodic State position after ack = %v, want ~500 (room authoritative position not updated)",
			next.State.Playstate.Position)
	}
}

// TestLateJoinerSeesCurrentState: a client joining after a seek must observe
// the room's current position in its first periodic State (defect (b): the
// room's authoritative position must reflect the seek).
func TestLateJoinerSeesCurrentState(t *testing.T) {
	env := newTestEnv(t)
	a := env.dial()
	a.hello("alice", "rl")

	// Paused seek to 300; wait for our own forced copy so the seek is fully
	// processed before the late joiner connects, then ack it.
	a.sendPlaystate(300, true, true)
	sa := a.waitForForcedState("own forced seek State", 3*time.Second)
	a.ackServerIgnore(sa.IgnoringOnTheFly.Server)

	b := env.dial()
	hb := b.hello("bob", "rl")
	if hb.Username != "bob" {
		t.Errorf("late joiner username = %q, want bob", hb.Username)
	}

	// The join handshake must tell bob about the existing member alice.
	b.waitFor("existing-member notice for alice", 3*time.Second, func(m *Message) bool {
		return m.Set != nil && m.Set.User != nil && m.Set.User["alice"] != nil
	})

	st := b.waitFor("first periodic State", 3*time.Second, func(m *Message) bool {
		return m.State != nil && m.State.Playstate != nil
	})
	if !st.State.Playstate.Paused {
		t.Errorf("late joiner periodic paused = false, want true")
	}
	if d := st.State.Playstate.Position - 300; d < -1 || d > 1 {
		t.Errorf("late joiner periodic position = %v, want ~300", st.State.Playstate.Position)
	}
}

// TestDisconnectRemovesUser: a disconnecting client must be broadcast as left
// and vanish from the List; its username becomes claimable again.
func TestDisconnectRemovesUser(t *testing.T) {
	env := newTestEnv(t)
	a := env.dial()
	a.hello("alice", "rd")
	b := env.dial()
	b.hello("bob", "rd")

	a.conn.Close()

	left := b.waitFor("left event", 3*time.Second, isLeftEvent)
	if left.Set.User["alice"] == nil {
		t.Errorf("left event is about %v, want alice", keysOfUserEvents(left.Set.User))
	}

	if got := b.pollRoomUsers("rd", []string{"bob"}, 3*time.Second); !equalStrings(got, []string{"bob"}) {
		t.Errorf("room users after disconnect = %v, want [bob]", got)
	}

	// The name must be claimable again by a fresh connection.
	c := env.dial()
	hc := c.hello("alice", "rd")
	if hc.Username != "alice" {
		t.Errorf("rejoin username = %q, want alice (name not released)", hc.Username)
	}
}

// TestDuplicateHelloNoGhost exercises defect (c): a second Hello on an
// already-logged-in connection must not register a second watcher; after the
// connection drops, no ghost user may stay behind in the room.
func TestDuplicateHelloNoGhost(t *testing.T) {
	env := newTestEnv(t)
	a := env.dial()
	first := a.hello("alice", "rc")
	if first.Username != "alice" {
		t.Fatalf("first Hello username = %q, want alice", first.Username)
	}
	second := a.hello("alice", "rc")
	if second.Username != "alice" {
		t.Errorf("second Hello username = %q, want alice (a second watcher was registered)", second.Username)
	}

	b := env.dial()
	b.hello("bob", "rc")

	if got := b.listRoomUsers("rc"); !equalStrings(got, []string{"alice", "bob"}) {
		t.Errorf("room users after duplicate Hello = %v, want [alice bob]", got)
	}

	a.conn.Close()
	b.waitFor("left event", 3*time.Second, isLeftEvent)
	if got := b.pollRoomUsers("rc", []string{"bob"}, 3*time.Second); !equalStrings(got, []string{"bob"}) {
		t.Errorf("room users after disconnect = %v, want [bob] (ghost user left behind)", got)
	}
}
