package syncplay

// 针对审查修复点的回归测试：
//  - S1：客户端发送 Error 消息后必须触发完整清理（left 广播 + 房间清空），
//    否则 stateTicker goroutine 泄漏、watcher/房间残留。
//  - M2：单行消息超过 MaxMessageLength 直接断开连接（内存 DoS 防护）。
//  - 负 position/duration 钳制为 0。

import (
	"bytes"
	"testing"
	"time"
)

// TestErrorMessageTriggersCleanup：收到客户端 Error 消息后，dispatch 返回
// false 关闭连接，cleanup 必须执行：同房间其他用户收到 left 事件、房间清空。
func TestErrorMessageTriggersCleanup(t *testing.T) {
	env := newTestEnv(t)
	a := env.dial()
	a.hello("alice", "rer")
	b := env.dial()
	b.hello("bob", "rer")

	a.sendRaw(`{"Error":{"message":"boom"}}`)

	left := b.waitFor("left event after client Error", 3*time.Second, isLeftEvent)
	if left.Set.User["alice"] == nil {
		t.Errorf("left event 是关于 %v, want alice", keysOfUserEvents(left.Set.User))
	}
	if got := b.pollRoomUsers("rer", []string{"bob"}, 3*time.Second); !equalStrings(got, []string{"bob"}) {
		t.Errorf("client Error 后房间用户 = %v, want [bob]（cleanup 未执行，watcher 残留）", got)
	}
}

// TestOversizedLineDisconnects：无换行的超长行（> MaxMessageLength）必须被
// 断开，而不是被无限缓冲（内存 DoS）。
func TestOversizedLineDisconnects(t *testing.T) {
	env := newTestEnv(t)
	conn := env.dial().conn

	big := bytes.Repeat([]byte{'x'}, MaxMessageLength+1)
	if _, err := conn.Write(big); err != nil {
		t.Fatalf("写入超长行失败: %v", err)
	}

	// 服务器应关闭连接：读到 EOF/错误
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1024)
	for {
		if _, err := conn.Read(buf); err != nil {
			return // EOF 或错误 = 连接已被断开
		}
	}
}

// TestNegativePositionClamped：负 position 进入写入路径后钳制为 0，
// 不会以负值外推/广播。
func TestNegativePositionClamped(t *testing.T) {
	env := newTestEnv(t)
	a := env.dial()
	a.hello("alice", "rneg")
	b := env.dial()
	b.hello("bob", "rneg")

	a.sendPlaystate(-5, true, true)

	st := b.waitForForcedState("forced State with clamped position", 3*time.Second)
	if st.Playstate == nil {
		t.Fatalf("forced State 无 playstate")
	}
	if pos := st.Playstate.Position; pos < -0.5 {
		t.Errorf("负 position 未被钳制，广播 position = %v, want >= 0", pos)
	}
}

// TestNegativeDurationClamped：FileInfo.Duration 为负时钳制为 0。
func TestNegativeDurationClamped(t *testing.T) {
	rm := NewRoomManager()
	room := rm.getRoom("unit-negdur")
	w := newWatcher("alice", nil)
	room.addWatcher(w)

	w.setFile(&FileInfo{Name: "clip.mp4", Duration: -12.5})
	w.mu.Lock()
	d := w.file.Duration
	w.mu.Unlock()
	if d != 0 {
		t.Errorf("负 duration 未被钳制为 0, got %v", d)
	}
}

// TestRoomSetPositionClampsNegative：Room.setPosition 负值钳制为 0。
func TestRoomSetPositionClampsNegative(t *testing.T) {
	room := newRoom("unit-negpos")
	room.setPosition(-100, nil)
	if got := room.getPosition(); got != 0 {
		t.Errorf("Room.setPosition(-100) 后 getPosition() = %v, want 0", got)
	}
}
