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
	if pos < 0 {
		pos = 0 // 负 position 钳制为 0，防止进入外推/广播
	}
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
	// 已清理的 client（c.closed=true）不得被在途 handleSet 的 moveWatcher
	// 重新加回房间：清理后不会再有任何机会移除，会留下幽灵 watcher。
	if w.client != nil && w.client.closed.Load() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Sync new watcher to room position
	if len(r.watchers) > 0 {
		w.setPosition(r.getPositionLocked())
	}
	r.watchers[w.name] = w
	w.mu.Lock()
	w.room = r
	w.mu.Unlock()
}

func (r *Room) removeWatcher(w *Watcher) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Only clear the slot if this exact watcher still owns it: a newer
	// watcher registered under the same name must not be evicted.
	if r.watchers[w.name] == w {
		delete(r.watchers, w.name)
	}
	w.mu.Lock()
	w.room = nil
	w.mu.Unlock()
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
	// 拒绝已断开 client 的 watcher 迁移（见 addWatcher 的说明）
	if w.client != nil && w.client.closed.Load() {
		return
	}
	rm.mu.Lock()
	w.mu.Lock()
	oldRoom := w.room
	w.mu.Unlock()
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
	w.mu.Lock()
	oldRoom := w.room
	w.mu.Unlock()
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
	sender.mu.Lock()
	room := sender.room
	sender.mu.Unlock()
	if room == nil {
		return
	}
	watchers := room.getWatchers()
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
	client        *Client // back-reference for sending messages
	version       string
	features      *ClientFeatures

	// mu 保护以下可变字段：room/file/position/ready/lastUpdatedOn 会被
	// 本连接 goroutine 写入、被其他连接 goroutine（handleList/广播）与
	// 本连接 stateTicker goroutine 读取。锁序：Room.mu → Watcher.mu，
	// 反向不存在（读取处均在 w.mu 内快照、解锁后再访问 Room）。
	mu            sync.Mutex
	room          *Room
	file          *FileInfo
	position      float64
	ready         *bool
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
	if f != nil {
		if f.Name != "" {
			f.Name = truncateText(f.Name, MaxFilenameLength)
		}
		if f.Duration < 0 {
			f.Duration = 0 // 负 duration 钳制为 0
		}
	}
	w.mu.Lock()
	w.file = f
	w.mu.Unlock()
}

func (w *Watcher) setPosition(pos float64) {
	if pos < 0 {
		pos = 0 // 负 position 钳制为 0，防止进入外推/广播
	}
	w.mu.Lock()
	w.position = pos
	w.mu.Unlock()
}

// getPosition 返回 watcher 的当前位置。若房间处于播放中，按 lastUpdatedOn
// 外推；暂停则返回存储值。锁内快照字段后解锁再访问房间，避免锁序反转。
func (w *Watcher) getPosition() float64 {
	w.mu.Lock()
	room := w.room
	pos := w.position
	lastUpdated := w.lastUpdatedOn
	w.mu.Unlock()
	if room == nil {
		return pos
	}
	if room.isPlaying() {
		return pos + time.Since(lastUpdated).Seconds()
	}
	return pos
}

func (w *Watcher) setReady(ready bool) {
	w.mu.Lock()
	w.ready = &ready
	w.mu.Unlock()
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
	w.mu.Lock()
	w.lastUpdatedOn = time.Now()
	room := w.room
	w.mu.Unlock()

	pauseChanged := false
	if paused != nil && room != nil {
		pauseChanged = (room.isPaused() && !*paused) || (!room.isPaused() && *paused)
		if pauseChanged {
			room.setPaused(*paused, w)
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
