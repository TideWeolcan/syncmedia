package syncplay

import (
	"sync"
	"time"
)

const (
	StatePaused  = 0
	StatePlaying = 1
)

// RoomManager owns all rooms and handles watcher lifecycle.
type RoomManager struct {
	mu    sync.RWMutex
	rooms map[string]*Room
	// names holds every claimed username: reserved by findFreeUsername,
	// released by removeWatcher. It makes name allocation atomic.
	names map[string]bool
}

func NewRoomManager() *RoomManager {
	return &RoomManager{
		rooms: make(map[string]*Room),
		names: make(map[string]bool),
	}
}

// Room represents a syncplay room containing watchers.
type Room struct {
	mu         sync.Mutex
	name       string
	watchers   map[string]*Watcher
	playState  int // StatePaused or StatePlaying
	position   float64
	setBy      *Watcher
	lastUpdate time.Time
}

func newRoom(name string) *Room {
	return &Room{
		name:       name,
		watchers:   make(map[string]*Watcher),
		playState:  StatePaused,
		lastUpdate: time.Now(),
	}
}

func (r *Room) getName() string { return r.name }

func (r *Room) isPlaying() bool { return r.playState == StatePlaying }
func (r *Room) isPaused() bool  { return r.playState == StatePaused }

// getPosition returns the room's current playback position.
// If playing, extrapolate from lastUpdate; if paused, return stored position.
func (r *Room) getPosition() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getPositionLocked()
}

// getPositionLocked is the lock-free version, caller must hold r.mu.
func (r *Room) getPositionLocked() float64 {
	age := time.Since(r.lastUpdate).Seconds()
	if r.playState == StatePlaying {
		return r.position + age
	}
	return r.position
}

func (r *Room) getSetBy() *Watcher {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.setBy
}

func (r *Room) setPaused(paused bool, setBy *Watcher) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// When pausing, freeze position at current extrapolated value
	if paused {
		age := time.Since(r.lastUpdate).Seconds()
		r.position = r.position + age
		r.playState = StatePaused
	} else {
		r.playState = StatePlaying
	}
	r.setBy = setBy
	r.lastUpdate = time.Now()
}

func (r *Room) setPosition(pos float64, setBy *Watcher) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.position = pos
	r.setBy = setBy
	r.lastUpdate = time.Now()
	for _, w := range r.watchers {
		w.setPosition(pos)
	}
}

func (r *Room) addWatcher(w *Watcher) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Sync new watcher to room position
	if len(r.watchers) > 0 {
		w.setPosition(r.getPositionLocked())
	}
	r.watchers[w.name] = w
	w.room = r
}

func (r *Room) removeWatcher(w *Watcher) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Only clear the slot if this exact watcher still owns it: a newer
	// watcher registered under the same name must not be evicted.
	if r.watchers[w.name] == w {
		delete(r.watchers, w.name)
	}
	w.room = nil
	if len(r.watchers) == 0 {
		r.position = 0
	}
}

func (r *Room) isEmpty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.watchers) == 0
}

func (r *Room) getWatchers() []*Watcher {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := make([]*Watcher, 0, len(r.watchers))
	for _, w := range r.watchers {
		list = append(list, w)
	}
	return list
}

// --- RoomManager methods ---

func (rm *RoomManager) getRoom(name string) *Room {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if r, ok := rm.rooms[name]; ok {
		return r
	}
	r := newRoom(name)
	rm.rooms[name] = r
	return r
}

func (rm *RoomManager) moveWatcher(w *Watcher, roomName string) {
	rm.mu.Lock()
	oldRoom := w.room
	rm.mu.Unlock()

	if oldRoom != nil {
		oldRoom.removeWatcher(w)
		rm.deleteRoomIfEmpty(oldRoom)
	}

	roomName = truncateText(roomName, MaxRoomNameLength)
	room := rm.getRoom(roomName)
	room.addWatcher(w)
}

func (rm *RoomManager) removeWatcher(w *Watcher) {
	rm.mu.Lock()
	oldRoom := w.room
	// Release the username reservation taken by findFreeUsername.
	delete(rm.names, w.name)
	rm.mu.Unlock()

	if oldRoom != nil {
		oldRoom.removeWatcher(w)
		rm.deleteRoomIfEmpty(oldRoom)
	}
}

func (rm *RoomManager) deleteRoomIfEmpty(room *Room) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if room.isEmpty() {
		delete(rm.rooms, room.name)
	}
}

// findFreeUsername returns a username that doesn't conflict with any claimed
// or room-registered name, and atomically reserves it, so concurrent claims
// always yield distinct names. removeWatcher releases the reservation.
func (rm *RoomManager) findFreeUsername(username string) string {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	username = truncateText(username, MaxUsernameLength)
	allNames := make(map[string]bool)
	for name := range rm.names {
		allNames[name] = true
	}
	for _, room := range rm.rooms {
		room.mu.Lock()
		for name := range room.watchers {
			allNames[name] = true
		}
		room.mu.Unlock()
	}
	// Strip trailing underscores if conflict
	if allNames[username] {
		for len(username) > 0 && username[len(username)-1] == '_' {
			username = username[:len(username)-1]
		}
		if username == "" {
			username = "_"
		}
	}
	for allNames[username] {
		username += "_"
	}
	rm.names[username] = true
	return username
}

// broadcastRoom sends a message to all watchers in the sender's room except sender.
func (rm *RoomManager) broadcastRoom(sender *Watcher, msg Message, includeSender bool) {
	if sender.room == nil {
		return
	}
	watchers := sender.room.getWatchers()
	for _, w := range watchers {
		if !includeSender && w == sender {
			continue
		}
		w.sendMessage(msg)
	}
}

// getAllWatchers returns all watchers across all rooms (for List command).
func (rm *RoomManager) getAllWatchers() []*Watcher {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	var all []*Watcher
	for _, room := range rm.rooms {
		room.mu.Lock()
		for _, w := range room.watchers {
			all = append(all, w)
		}
		room.mu.Unlock()
	}
	return all
}

// Watcher represents a connected user in a room.
type Watcher struct {
	name          string
	room          *Room
	file          *FileInfo
	position      float64
	ready         *bool
	client        *Client // back-reference for sending messages
	version       string
	features      *ClientFeatures
	lastUpdatedOn time.Time
}

func newWatcher(name string, client *Client) *Watcher {
	return &Watcher{
		name:          name,
		client:        client,
		lastUpdatedOn: time.Now(),
	}
}

func (w *Watcher) setFile(f *FileInfo) {
	if f != nil && f.Name != "" {
		f.Name = truncateText(f.Name, MaxFilenameLength)
	}
	w.file = f
}

func (w *Watcher) setPosition(pos float64) {
	w.position = pos
}

func (w *Watcher) getPosition() float64 {
	if w.room == nil {
		return w.position
	}
	if w.room.isPlaying() {
		elapsed := time.Since(w.lastUpdatedOn).Seconds()
		return w.position + elapsed
	}
	return w.position
}

func (w *Watcher) setReady(ready bool) {
	w.ready = &ready
}

func (w *Watcher) sendMessage(msg Message) {
	if w.client != nil {
		w.client.send(msg)
	}
}

// updateState is called when the server receives a State message from this
// watcher. position is nil when the client sent no playstate; a non-nil 0 is
// a legitimate seek to the start of the file.
func (w *Watcher) updateState(position *float64, paused *bool, doSeek *bool, messageAge float64) {
	w.lastUpdatedOn = time.Now()

	pauseChanged := false
	if paused != nil && w.room != nil {
		pauseChanged = (w.room.isPaused() && !*paused) || (!w.room.isPaused() && *paused)
		if pauseChanged {
			w.room.setPaused(*paused, w)
		}
	}

	if position != nil {
		adjusted := *position
		if paused != nil && !*paused {
			adjusted += messageAge
		}
		w.setPosition(adjusted)
	}

	if (doSeek != nil && *doSeek) || pauseChanged {
		// Force a position update broadcast to the room
		w.client.server.forcePositionUpdate(w, doSeek != nil && *doSeek, paused)
	}
}
