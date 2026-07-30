package syncplay

import (
	"time"
)

// PingService tracks RTT and forward delay for a single client connection.
// Mirrors the logic from syncplay's PingService class.
type PingService struct {
	rtt    float64 // most recent RTT
	avrRTT float64 // exponential moving average of RTT
	fd     float64 // forward delay
}

func newPingService() *PingService {
	return &PingService{}
}

// newTimestamp returns the current unix timestamp (seconds, float).
func (p *PingService) newTimestamp() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

// receiveMessage processes an incoming ping from the client.
// timestamp is the client's latencyCalculation (when they sent the message).
// senderRtt is the client's measurement of the RTT.
func (p *PingService) receiveMessage(timestamp, senderRtt float64) {
	if timestamp == 0 {
		return
	}
	now := p.newTimestamp()
	p.rtt = now - timestamp
	if p.rtt < 0 || senderRtt < 0 {
		return
	}
	if p.avrRTT == 0 {
		p.avrRTT = p.rtt
	} else {
		p.avrRTT = p.avrRTT*PingAvgWeight + p.rtt*(1-PingAvgWeight)
	}
	if senderRtt < p.rtt {
		p.fd = p.avrRTT/2 + (p.rtt - senderRtt)
	} else {
		p.fd = p.avrRTT / 2
	}
}

func (p *PingService) getRTT() float64       { return p.rtt }
func (p *PingService) getForwardDelay() float64 { return p.fd }

// startStateLoop begins the periodic state broadcast for a watcher.
// Every StateInterval seconds, the server sends a State message to the client.
func (c *Client) startStateLoop() {
	c.stateTicker = time.NewTicker(time.Duration(StateInterval) * time.Second)
	go func() {
		for {
			select {
			case <-c.stateTicker.C:
				c.sendPeriodicState()
			case <-c.done:
				return
			}
		}
	}()
}

// sendPeriodicState sends the current room state to this client.
func (c *Client) sendPeriodicState() {
	if !c.logged || c.watcher == nil || c.watcher.room == nil {
		return
	}

	// Check protocol timeout
	if time.Since(c.lastUpdatedOn).Seconds() > ProtocolTimeout {
		c.dropWithError("protocol timeout")
		return
	}

	room := c.watcher.room
	pos := room.getPosition()
	paused := room.isPaused()
	setBy := room.getSetBy()

	c.sendState(pos, paused, false, setBy, false)
}

// sendState constructs and sends a State message.
// position: room position to send
// paused: room pause state
// doSeek: whether to force a seek
// setBy: the watcher that set this state
// forced: if true, increment serverIgnoringOnTheFly
func (c *Client) sendState(position float64, paused bool, doSeek bool, setBy *Watcher, forced bool) {
	var processingTime float64
	if c.clientLatencyCalculationArrival.IsZero() {
		processingTime = 0
	} else {
		processingTime = time.Since(c.clientLatencyCalculationArrival).Seconds()
	}

	setByName := ""
	if setBy != nil {
		setByName = setBy.name
	}

	state := &StateMsg{
		Ping: &PingInfo{
			LatencyCalculation: c.ping.newTimestamp(),
			ServerRTT:          c.ping.getRTT(),
		},
		Playstate: &Playstate{
			Position: position,
			Paused:   paused,
			DoSeek:   doSeek,
			SetBy:    setByName,
		},
	}

	if c.clientLatencyCalculation != 0 {
		state.Ping.ClientLatencyCalculation = c.clientLatencyCalculation + processingTime
		c.clientLatencyCalculation = 0
	}

	if forced {
		c.serverIgnoringOnTheFly++
	}

	if c.serverIgnoringOnTheFly > 0 || c.clientIgnoringOnTheFly > 0 {
		state.IgnoringOnTheFly = &IgnoreInfo{}
		if c.serverIgnoringOnTheFly > 0 {
			state.IgnoringOnTheFly.Server = c.serverIgnoringOnTheFly
		}
		if c.clientIgnoringOnTheFly > 0 {
			state.IgnoringOnTheFly.Client = c.clientIgnoringOnTheFly
			c.clientIgnoringOnTheFly = 0
		}
	}

	// Only send if we don't have outstanding server ignores (unless forced)
	if c.serverIgnoringOnTheFly == 0 || forced {
		c.send(Message{State: state})
	}
}

// handleState processes an incoming State message from the client.
func (c *Client) handleState(state *StateMsg) {
	if state == nil {
		return
	}

	// Handle ignoringOnTheFly ack
	if state.IgnoringOnTheFly != nil {
		if state.IgnoringOnTheFly.Server > 0 {
			if c.serverIgnoringOnTheFly == state.IgnoringOnTheFly.Server {
				c.serverIgnoringOnTheFly = 0
			}
		}
		if state.IgnoringOnTheFly.Client > 0 {
			c.clientIgnoringOnTheFly = state.IgnoringOnTheFly.Client
		}
	}

	// Extract playstate. position stays nil when the client sent no
	// playstate at all — a real position of 0 is a legitimate seek target.
	var position *float64
	var paused *bool
	var doSeek *bool

	if state.Playstate != nil {
		pos := state.Playstate.Position
		position = &pos
		p := state.Playstate.Paused
		paused = &p
		if state.Playstate.DoSeek {
			s := true
			doSeek = &s
		}
	}

	// Handle ping
	if state.Ping != nil {
		latencyCalc := state.Ping.LatencyCalculation
		clientRtt := state.Ping.ClientRTT
		c.clientLatencyCalculation = state.Ping.ClientLatencyCalculation
		c.clientLatencyCalculationArrival = time.Now()
		c.ping.receiveMessage(latencyCalc, clientRtt)
	}

	// Update watcher state only if server isn't ignoring
	if c.serverIgnoringOnTheFly == 0 {
		fd := c.ping.getForwardDelay()
		c.watcher.updateState(position, paused, doSeek, fd)
	}
}
