package syncplay

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tjfoc/gmsm/gmtls"
)

// Server is the syncplay TCP server.
type Server struct {
	listener  net.Listener
	rooms     *RoomManager
	gmConfig *gmtls.Config // nil = no TLS support
	features  ServerFeatures
	password  string // MD5 hash, empty = no password
	bind      string // 监听地址，空=全接口
	wg        sync.WaitGroup
	mu        sync.Mutex
	closing   bool
	conns     map[net.Conn]struct{} // active connections, guarded by mu
	logger    *log.Logger
}

type ServerConfig struct {
	Port     int
	Password string
	Bind     string // 监听地址；空=全接口（:port）
	GMConfig *gmtls.Config
	Logger   *log.Logger
}

func NewServer(cfg ServerConfig) *Server {
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	return &Server{
		rooms:     NewRoomManager(),
		gmConfig: cfg.GMConfig,
		features:  DefaultServerFeatures(),
		password:  cfg.Password,
		bind:      cfg.Bind,
		conns:     make(map[net.Conn]struct{}),
		logger:    cfg.Logger,
	}
}

// Listen starts the TCP listener on the given port.
func (s *Server) Listen(port int) error {
	addr := fmt.Sprintf(":%d", port)
	if s.bind != "" {
		addr = net.JoinHostPort(s.bind, strconv.Itoa(port))
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听 %s: %w", addr, err)
	}
	s.listener = ln
	s.logger.Printf("syncplay 服务器监听 %s (TLS=%v)", addr, s.gmConfig != nil)
	return nil
}

// Serve accepts connections until the server is closed.
func (s *Server) Serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.mu.Lock()
			closing := s.closing
			s.mu.Unlock()
			if closing {
				return
			}
			s.logger.Printf("接收连接错误: %v", err)
			continue
		}
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			conn.Close()
			continue
		}
		s.conns[conn] = struct{}{}
		// wg.Add 与 conns 注册同锁：Close 复制到该 conn 即保证 Add 已完成，
		// 其 wg.Wait() 不会与 Add 并发、在 counter=0 时提前返回。
		s.wg.Add(1)
		s.mu.Unlock()
		go s.handleConnection(conn)
	}
}

// Close gracefully shuts down the server: it stops accepting new
// connections, force-closes every active connection so reader goroutines
// exit promptly, then waits for them. Bounded even while clients keep
// sending (each read resets the per-read deadline, so waiting alone is not).
func (s *Server) Close() error {
	s.mu.Lock()
	s.closing = true
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	if s.listener != nil {
		s.listener.Close()
	}
	for _, c := range conns {
		c.Close()
	}
	s.wg.Wait()
	return nil
}

// Client represents a single connected syncplay client.
type Client struct {
	conn                          net.Conn
	server                        *Server
	reader                        *bufio.Reader
	writeMu                       sync.Mutex
	watcher                       *Watcher
	logged                        bool
	ping                          *PingService
	serverIgnoringOnTheFly        int
	clientIgnoringOnTheFly        int
	clientLatencyCalculation      float64
	clientLatencyCalculationArrival time.Time
	lastUpdatedOn                 time.Time
	stateMu                       sync.Mutex // 保护 lastUpdatedOn/延迟计算/ignoring 计数/ping（主循环与 stateTicker 双 goroutine 共享）
	stateTicker                   *time.Ticker
	done                          chan struct{}
	closed                        atomic.Bool
	closeMu                       sync.Mutex
}

func (s *Server) handleConnection(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()

	client := &Client{
		conn:          conn,
		server:        s,
		reader:        bufio.NewReaderSize(conn, MaxMessageLength+1),
		ping:          newPingService(),
		lastUpdatedOn: time.Now(),
		done:          make(chan struct{}),
	}
	// 统一清理入口：任何返回路径（dispatch false、读错误、超长行）都保证
	// 执行 cleanup，杜绝 stateTicker goroutine 泄漏与 watcher/房间残留。
	defer client.cleanup()

	s.logger.Printf("新连接: %s", conn.RemoteAddr())

	// Read lines and dispatch messages
	for {
		conn.SetReadDeadline(time.Now().Add(time.Duration(ProtocolTimeout * float64(time.Second))))
		line, err := readLineBounded(client.reader, MaxMessageLength)
		if err != nil {
			if errors.Is(err, errLineTooLong) {
				s.logger.Printf("消息超过 %d 字节 %s，断开连接", MaxMessageLength, conn.RemoteAddr())
			} else if err != io.EOF {
				s.logger.Printf("读取错误 %s: %v", conn.RemoteAddr(), err)
			}
			client.cleanup()
			return
		}

		// Trim whitespace
		line = trimSpace(line)
		if len(line) == 0 {
			continue
		}

		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			s.logger.Printf("JSON 解析错误 %s: %v", conn.RemoteAddr(), err)
			client.dropWithError("not-json-server-error")
			return
		}

		if !client.dispatch(msg) {
			// dispatch returned false = connection should close
			return
		}
	}
}

// errLineTooLong 表示单行消息超过 MaxMessageLength，连接应被断开。
var errLineTooLong = errors.New("line too long")

// readLineBounded 读取一行（\n 结尾）。与 ReadBytes 不同，它对行长度设上限：
// 超过 maxLen 返回 errLineTooLong，防止恶意客户端用无换行的超长行耗尽内存。
func readLineBounded(r *bufio.Reader, maxLen int) ([]byte, error) {
	var line []byte
	for {
		frag, err := r.ReadSlice('\n')
		if len(line)+len(frag) > maxLen {
			return nil, errLineTooLong
		}
		line = append(line, frag...)
		if err == nil {
			return line, nil
		}
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
}

// dispatch routes a message to the appropriate handler.
// Returns false if the connection should be terminated.
func (c *Client) dispatch(msg Message) bool {
	if msg.TLS != nil {
		c.handleTLS(msg.TLS)
		return true // After TLS upgrade, the connection continues
	}

	if msg.Hello != nil {
		c.handleHello(msg.Hello)
		return true
	}

	if !c.logged {
		c.dropWithError("not-known-server-error")
		return false
	}

	c.stateMu.Lock()
	c.lastUpdatedOn = time.Now()
	c.stateMu.Unlock()

	if msg.State != nil {
		c.handleState(msg.State)
		return true
	}

	if msg.Set != nil {
		c.handleSet(msg.Set)
		return true
	}

	if msg.Chat != nil {
		c.handleChat(msg.Chat)
		return true
	}

	if msg.List != nil {
		c.handleList()
		return true
	}

	if msg.Error != nil {
		c.server.logger.Printf("客户端错误 %s: %s", c.conn.RemoteAddr(), msg.Error.Message)
		return false
	}

	return true
}

// handleTLS processes STARTTLS negotiation.
func (c *Client) handleTLS(msg *TLSMsg) {
	if msg.StartTLS != "send" {
		return
	}
	// Can only upgrade before login
	if c.logged {
		c.send(Message{TLS: &TLSMsg{StartTLS: "false"}})
		return
	}
	if c.server.gmConfig == nil {
		c.send(Message{TLS: &TLSMsg{StartTLS: "false"}})
		return
	}
	// 升级前若 bufio 已预读数据（客户端在握手确认前就发送了后续字节），
	// 无法安全交给 TLS 层，拒绝升级
	if c.reader.Buffered() > 0 {
		c.send(Message{TLS: &TLSMsg{StartTLS: "false"}})
		return
	}
	// 握手单独设置读/写超时（30s），不复用读循环 SetReadDeadline 的剩余窗口，
	// 慢网络下不会在握手中途被陈旧 deadline 掐断
	c.conn.SetDeadline(time.Now().Add(30 * time.Second))
	tlsConn := gmtls.Server(c.conn, c.server.gmConfig)
	if err := tlsConn.Handshake(); err != nil {
		c.conn.SetDeadline(time.Time{})
		c.server.logger.Printf("TLS 握手失败 %s: %v", c.conn.RemoteAddr(), err)
		c.cleanup()
		return
	}
	c.conn.SetDeadline(time.Time{}) // 清除握手 deadline，读循环会重新设置读超时
	c.conn = tlsConn
	c.reader = bufio.NewReaderSize(tlsConn, MaxMessageLength+1)
	c.server.logger.Printf("TLS 已升级 %s", c.conn.RemoteAddr())
}

// handleHello processes the Hello handshake.
func (c *Client) handleHello(hello *HelloMsg) {
	// A repeated Hello on an already-logged-in connection must not register
	// a second watcher: the first one would linger in the room as a ghost
	// user and its state ticker would leak. Re-ack the current identity.
	if c.logged {
		c.sendHelloResponse(hello.Version)
		return
	}

	if hello.Username == "" || hello.Version == "" {
		c.dropWithError("hello-server-error")
		return
	}

	// Check room
	roomName := ""
	if hello.Room != nil {
		roomName = hello.Room.Name
	}
	if roomName == "" {
		c.dropWithError("hello-server-error")
		return
	}

	// Check password
	if c.server.password != "" {
		if hello.Password != c.server.password {
			c.dropWithError("password-required-server-error")
			return
		}
	}

	// Parse client features
	var features *ClientFeatures
	if hello.Features != nil {
		features = hello.Features
	} else {
		features = &ClientFeatures{}
	}

	// Create watcher
	username := c.server.rooms.findFreeUsername(hello.Username)
	w := newWatcher(username, c)
	w.version = hello.Version
	w.features = features
	c.watcher = w
	c.logged = true

	// Join room
	roomName = truncateText(roomName, MaxRoomNameLength)
	c.server.rooms.moveWatcher(w, roomName)

	// Send Hello response
	c.sendHelloResponse(hello.Version)

	// Notify room of join
	c.sendJoinMessage()

	// Start state sync loop
	c.startStateLoop()

	c.server.logger.Printf("用户 '%s' 加入房间 '%s' (版本 %s)", username, roomName, hello.Version)
}

// sendHelloResponse sends the server's Hello message back.
// Uses raw JSON to include ServerFeatures (which differs from ClientFeatures).
func (c *Client) sendHelloResponse(clientVersion string) {
	roomName := ""
	c.watcher.mu.Lock()
	room := c.watcher.room
	c.watcher.mu.Unlock()
	if room != nil {
		roomName = room.getName()
	}

	helloData := map[string]interface{}{
		"Hello": map[string]interface{}{
			"username":    c.watcher.name,
			"room":        map[string]string{"name": roomName},
			"version":     clientVersion, // echo client version for BC
			"realversion": ServerVersion,
			"motd":        "",
			"features": map[string]interface{}{
				"isolateRooms":        c.server.features.IsolateRooms,
				"readiness":           c.server.features.Readiness,
				"managedRooms":        c.server.features.ManagedRooms,
				"persistentRooms":     c.server.features.PersistentRooms,
				"chat":                c.server.features.Chat,
				"maxChatMessageLength": c.server.features.MaxChatLength,
				"maxUsernameLength":   c.server.features.MaxUsernameLength,
				"maxRoomNameLength":   c.server.features.MaxRoomNameLength,
				"maxFilenameLength":   c.server.features.MaxFilenameLength,
				"setOthersReadiness":  c.server.features.SetOthersReadiness,
			},
		},
	}

	raw, err := json.Marshal(helloData)
	if err != nil {
		log.Printf("序列化 Hello 消息失败: %v", err)
		return
	}
	c.writeRaw(raw)
}

// sendJoinMessage broadcasts to the room that this watcher joined.
func (c *Client) sendJoinMessage() {
	if c.watcher == nil {
		return
	}
	c.watcher.mu.Lock()
	room := c.watcher.room
	c.watcher.mu.Unlock()
	if room == nil {
		return
	}

	// Notify others in room
	roomName := room.getName()

	// Send to all other watchers in room
	watchers := room.getWatchers()
	for _, w := range watchers {
		if w == c.watcher {
			continue
		}
		// Include this watcher's file if set
		ev := &UserEvent{
			Room:  &RoomRef{Name: roomName},
			Event: &EventInfo{Joined: true},
		}
		w.mu.Lock()
		file := w.file
		w.mu.Unlock()
		if file != nil {
			ev.File = file
		}
		w.sendMessage(Message{Set: &SetMsg{
			User: map[string]*UserEvent{c.watcher.name: ev},
		}})
	}

	// Send existing room members to the new watcher
	for _, w := range watchers {
		if w == c.watcher {
			continue
		}
		ev := &UserEvent{
			Room: &RoomRef{Name: roomName},
		}
		w.mu.Lock()
		file := w.file
		w.mu.Unlock()
		if file != nil {
			ev.File = file
		}
		c.send(Message{Set: &SetMsg{
			User: map[string]*UserEvent{w.name: ev},
		}})
	}

	// Broadcast readiness state of existing members to new watcher
	for _, w := range watchers {
		if w == c.watcher {
			continue
		}
		w.mu.Lock()
		ready := w.ready
		w.mu.Unlock()
		if ready != nil {
			c.send(Message{Set: &SetMsg{
				Ready: &ReadySet{
					Username:         w.name,
					IsReady:          *ready,
					ManuallyInitiated: false,
				},
			}})
		}
	}
}

// sendLeftMessage broadcasts to the room that this watcher left.
func (c *Client) sendLeftMessage() {
	if c.watcher == nil {
		return
	}
	c.watcher.mu.Lock()
	room := c.watcher.room
	c.watcher.mu.Unlock()
	if room == nil {
		return
	}
	roomName := room.getName()
	c.server.rooms.broadcastRoom(c.watcher, Message{
		Set: &SetMsg{
			User: map[string]*UserEvent{
				c.watcher.name: {
					Room:  &RoomRef{Name: roomName},
					Event: &EventInfo{Left: true},
				},
			},
		},
	}, false)
}

// handleSet processes Set commands from the client.
func (c *Client) handleSet(set *SetMsg) {
	// 清理后的连接（c.closed=true，ticker 超时踢人等）不再处理在途消息：
	// 否则 moveWatcher 会把已断开 watcher 重新加回房间，且无人再清理。
	if c.closed.Load() {
		return
	}
	if set.Room != nil {
		// Room switch
		roomName := set.Room.Name
		if strings.TrimSpace(roomName) == "" {
			// 与 Hello 同样的校验：空白房间名拒绝，防止进入空名房间
			c.dropWithError("hello-server-error")
			return
		}
		c.sendLeftMessage()
		c.server.rooms.moveWatcher(c.watcher, roomName)
		c.sendRoomSwitchMessage()
	}

	if set.File != nil {
		// File update
		c.watcher.setFile(set.File)
		// Broadcast file change to room（room/file 锁内快照，见 cleanup 的 ticker 路径）
		c.watcher.mu.Lock()
		room := c.watcher.room
		file := c.watcher.file
		c.watcher.mu.Unlock()
		if room != nil {
			c.server.rooms.broadcastRoom(c.watcher, Message{
				Set: &SetMsg{
					User: map[string]*UserEvent{
						c.watcher.name: {
							Room: &RoomRef{Name: room.getName()},
							File: file,
						},
					},
				},
			}, true)
		}
	}

	if set.Ready != nil {
		// Ready state change
		c.watcher.setReady(set.Ready.IsReady)
		// Broadcast to room
		c.server.rooms.broadcastRoom(c.watcher, Message{
			Set: &SetMsg{
				Ready: &ReadySet{
					Username:          c.watcher.name,
					IsReady:           set.Ready.IsReady,
					ManuallyInitiated: set.Ready.ManuallyInitiated,
				},
			},
		}, false)
	}

	// playlistChange and playlistIndex: accept but don't do much
	// (Kazumi doesn't use playlists, but standard clients might)
}

// sendRoomSwitchMessage notifies the room of a watcher's room switch.
func (c *Client) sendRoomSwitchMessage() {
	if c.watcher == nil {
		return
	}
	c.watcher.mu.Lock()
	room := c.watcher.room
	c.watcher.mu.Unlock()
	if room == nil {
		return
	}
	roomName := room.getName()

	// Broadcast user's new room to everyone in the room
	c.server.rooms.broadcastRoom(c.watcher, Message{
		Set: &SetMsg{
			User: map[string]*UserEvent{
				c.watcher.name: {
					Room: &RoomRef{Name: roomName},
				},
			},
		},
	}, true)

	// Send existing members to the switched watcher
	watchers := room.getWatchers()
	for _, w := range watchers {
		if w == c.watcher {
			continue
		}
		ev := &UserEvent{
			Room: &RoomRef{Name: roomName},
		}
		w.mu.Lock()
		file := w.file
		w.mu.Unlock()
		if file != nil {
			ev.File = file
		}
		c.send(Message{Set: &SetMsg{
			User: map[string]*UserEvent{w.name: ev},
		}})
	}
}

// handleChat relays a chat message to the room.
func (c *Client) handleChat(chat *ChatMsg) {
	msg := truncateText(chat.Message, c.server.features.MaxChatLength)
	chatMsg := &ChatMsg{
		Username: c.watcher.name,
		Message:  msg,
	}
	c.server.rooms.broadcastRoom(c.watcher, Message{Chat: chatMsg}, true)
}

// handleList sends the room/user list to the client.
func (c *Client) handleList() {
	allWatchers := c.server.rooms.getAllWatchers()
	userlist := make(map[string]map[string]interface{})
	for _, w := range allWatchers {
		// 快照他人 watcher 的可变字段（room/file/ready），避免与断开清理竞态
		w.mu.Lock()
		room := w.room
		var roomName string
		if room != nil {
			roomName = room.getName()
		}
		file := w.file
		isReady := false
		if w.ready != nil {
			isReady = *w.ready
		}
		w.mu.Unlock()
		if room == nil {
			continue
		}
		if _, ok := userlist[roomName]; !ok {
			userlist[roomName] = make(map[string]interface{})
		}
		fileInfo := map[string]interface{}{}
		if file != nil {
			fileInfo = map[string]interface{}{
				"name":     file.Name,
				"duration": file.Duration,
				"size":     file.Size,
			}
		}
		userlist[roomName][w.name] = map[string]interface{}{
			"position":  0,
			"file":      fileInfo,
			"controller": false,
			"isReady":   isReady,
			"features":  map[string]interface{}{},
		}
	}
	c.send(Message{List: userlist})
}

// forcePositionUpdate persists the initiator's position as the room's
// authoritative position and broadcasts a forced state update to the room.
func (s *Server) forcePositionUpdate(watcher *Watcher, doSeek bool, watcherPauseState *bool) {
	watcher.mu.Lock()
	room := watcher.room
	watcher.mu.Unlock()
	if room == nil {
		return
	}
	pos := watcher.getPosition()
	paused := room.isPaused()
	setBy := watcher

	// Write the room's authoritative position (and sync every watcher to
	// it), so subsequent periodic States don't snap back to a stale value.
	room.setPosition(pos, watcher)

	watchers := room.getWatchers()
	for _, w := range watchers {
		if w.client == nil {
			continue
		}
		w.client.sendState(pos, paused, doSeek, setBy, true)
	}
}

// --- Client I/O ---

func (c *Client) send(msg Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	c.writeRaw(data)
}

func (c *Client) writeRaw(data []byte) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed.Load() {
		return
	}
	data = append(data, '\n')
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.conn.Write(data); err != nil {
		c.server.logger.Printf("写入错误: %v", err)
	}
}

func (c *Client) dropWithError(errorKey string) {
	msg := getErrorMessage(errorKey)
	c.send(Message{Error: &ErrorMsg{Message: msg}})
	c.cleanup()
}

func (c *Client) cleanup() {
	c.closeMu.Lock()
	if c.closed.Load() {
		c.closeMu.Unlock()
		return
	}
	c.closed.Store(true)
	close(c.done)

	if c.stateTicker != nil {
		c.stateTicker.Stop()
	}
	c.closeMu.Unlock()

	// 广播 left 消息放到 closeMu 之外：网络写可能阻塞（每对端 10s 写超时），
	// 若持锁会拖慢 Server.Close 的 wg.Wait。c.closed 已在锁内置位，
	// 后续 addWatcher 的 closed 检查会拒绝把本 watcher 加回房间。
	if c.watcher != nil {
		c.sendLeftMessage()
		c.server.rooms.removeWatcher(c.watcher)
		c.server.logger.Printf("用户 '%s' 已断开", c.watcher.name)
	}

	c.conn.Close()
}

// trimSpace removes leading/trailing whitespace including \r\n
func trimSpace(data []byte) []byte {
	start := 0
	end := len(data)
	for start < end && (data[start] == ' ' || data[start] == '\t' || data[start] == '\r' || data[start] == '\n') {
		start++
	}
	for end > start && (data[end-1] == ' ' || data[end-1] == '\t' || data[end-1] == '\r' || data[end-1] == '\n') {
		end--
	}
	return data[start:end]
}

// getErrorMessage maps syncplay error keys to human-readable messages.
func getErrorMessage(key string) string {
	messages := map[string]string{
		"hello-server-error":          "Hello 消息格式错误。",
		"not-known-server-error":      "未知服务器错误 — 请先发送 Hello。",
		"password-required-server-error": "需要密码。",
		"wrong-password-server-error": "密码错误。",
		"not-json-server-error":       "消息不是有效的 JSON。",
		"line-decode-server-error":    "行无法解码为 UTF-8。",
		"unknown-command-server-error": "收到未知的命令类型。",
	}
	if msg, ok := messages[key]; ok {
		return msg
	}
	return key
}
