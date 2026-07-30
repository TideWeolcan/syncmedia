package syncplay

// Protocol constants (from syncplay/constants.py)
const (
	DefaultPort       = 8999
	ProtocolTimeout   = 12.5  // seconds
	StateInterval     = 1     // seconds
	PingAvgWeight     = 0.7   // moving average weight
	MaxChatLength     = 150
	MaxUsernameLength = 16
	MaxRoomNameLength = 35
	MaxFilenameLength = 250
	ServerVersion     = "1.7.5"
)

// Message is the top-level envelope. Each syncplay message is a single-key
// JSON object like {"Hello": {...}} or {"State": {...}}.
// Only one field is non-nil at a time.
type Message struct {
	Hello *HelloMsg `json:"Hello,omitempty"`
	Set   *SetMsg   `json:"Set,omitempty"`
	List  any       `json:"List,omitempty"`
	State *StateMsg `json:"State,omitempty"`
	Chat  *ChatMsg  `json:"Chat,omitempty"`
	Error *ErrorMsg `json:"Error,omitempty"`
	TLS   *TLSMsg   `json:"TLS,omitempty"`
}

// TLSMsg handles STARTTLS negotiation.
type TLSMsg struct {
	StartTLS string `json:"startTLS,omitempty"`
}

// HelloMsg is the handshake message exchanged after (optional) TLS upgrade.
type HelloMsg struct {
	Username    string          `json:"username"`
	Room        *RoomRef        `json:"room,omitempty"`
	Version     string          `json:"version"`
	RealVersion string          `json:"realversion,omitempty"`
	Password    string          `json:"password,omitempty"`
	Features    *ClientFeatures `json:"features,omitempty"`
	Motd        string          `json:"motd,omitempty"`
}

type RoomRef struct {
	Name string `json:"name"`
}

// ClientFeatures declares what the client supports.
type ClientFeatures struct {
	SharedPlaylists bool        `json:"sharedPlaylists"`
	Chat            bool        `json:"chat"`
	FeatureList     bool        `json:"featureList"`
	Readiness       bool        `json:"readiness"`
	ManagedRooms    bool        `json:"managedRooms"`
	PersistentRooms bool        `json:"persistentRooms"`
	UiMode          string      `json:"uiMode,omitempty"`
	Raw             interface{} `json:"-"`
}

// ServerFeatures is sent in the server's Hello response.
type ServerFeatures struct {
	IsolateRooms       bool `json:"isolateRooms"`
	Readiness          bool `json:"readiness"`
	ManagedRooms       bool `json:"managedRooms"`
	PersistentRooms    bool `json:"persistentRooms"`
	Chat               bool `json:"chat"`
	MaxChatLength      int  `json:"maxChatMessageLength"`
	MaxUsernameLength  int  `json:"maxUsernameLength"`
	MaxRoomNameLength  int  `json:"maxRoomNameLength"`
	MaxFilenameLength  int  `json:"maxFilenameLength"`
	SetOthersReadiness bool `json:"setOthersReadiness"`
}

// SetMsg carries various "set" operations. Different sub-fields can be present.
type SetMsg struct {
	Room           *RoomRef             `json:"room,omitempty"`
	File           *FileInfo            `json:"file,omitempty"`
	User           map[string]*UserEvent `json:"user,omitempty"`
	Ready          *ReadySet            `json:"ready,omitempty"`
	PlaylistChange *PlaylistChangeSet   `json:"playlistChange,omitempty"`
	PlaylistIndex  *PlaylistIndexSet    `json:"playlistIndex,omitempty"`
}

type FileInfo struct {
	Name     string  `json:"name,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	Size     int64   `json:"size,omitempty"`
}

type UserEvent struct {
	Room  *RoomRef  `json:"room,omitempty"`
	File  *FileInfo `json:"file,omitempty"`
	Event *EventInfo `json:"event,omitempty"`
}

type EventInfo struct {
	Left   bool `json:"left,omitempty"`
	Joined bool `json:"joined,omitempty"`
}

type ReadySet struct {
	Username         string `json:"username"`
	IsReady          bool   `json:"isReady"`
	ManuallyInitiated bool  `json:"manuallyInitiated"`
	SetBy            string `json:"setBy,omitempty"`
}

type PlaylistChangeSet struct {
	User  string      `json:"user"`
	Files interface{} `json:"files"`
}

type PlaylistIndexSet struct {
	User  string `json:"user"`
	Index int    `json:"index"`
}

// StateMsg carries playstate + ping for synchronisation.
type StateMsg struct {
	Ping            *PingInfo    `json:"ping,omitempty"`
	Playstate       *Playstate   `json:"playstate,omitempty"`
	IgnoringOnTheFly *IgnoreInfo `json:"ignoringOnTheFly,omitempty"`
}

type PingInfo struct {
	LatencyCalculation      float64 `json:"latencyCalculation"`
	ServerRTT               float64 `json:"serverRtt,omitempty"`
	ClientLatencyCalculation float64 `json:"clientLatencyCalculation,omitempty"`
	ClientRTT               float64 `json:"clientRtt,omitempty"`
}

type Playstate struct {
	Position float64 `json:"position"`
	Paused   bool    `json:"paused"`
	DoSeek   bool    `json:"doSeek,omitempty"`
	SetBy    string  `json:"setBy,omitempty"`
}

type IgnoreInfo struct {
	Server int `json:"server,omitempty"`
	Client int `json:"client,omitempty"`
}

// ChatMsg is a chat message relayed within a room.
type ChatMsg struct {
	Username string `json:"username"`
	Message  string `json:"message"`
}

// ErrorMsg carries a protocol error before disconnecting.
type ErrorMsg struct {
	Message string `json:"message"`
}

// --- Helpers ---

// DefaultServerFeatures returns the features our Go syncplay server advertises.
func DefaultServerFeatures() ServerFeatures {
	return ServerFeatures{
		IsolateRooms:       false,
		Readiness:          true,
		ManagedRooms:       true,
		PersistentRooms:    false,
		Chat:               true,
		MaxChatLength:      MaxChatLength,
		MaxUsernameLength:  MaxUsernameLength,
		MaxRoomNameLength:  MaxRoomNameLength,
		MaxFilenameLength:  MaxFilenameLength,
		SetOthersReadiness: true,
	}
}

// truncateText truncates s to at most maxLen runes.
func truncateText(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}
