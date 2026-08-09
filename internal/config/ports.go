package config

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
)

// 内置首选端口：用户未配置端口（0）时优先尝试。
const (
	PreferredSyncplayPort = 8999
	PreferredWebPort      = 8080
)

// commonServicePorts 常见服务端口避让表：随机避让时排除，
// 避免与 ssh/http/mysql 等常用服务冲突。
var commonServicePorts = map[int]bool{
	22: true, 25: true, 53: true, 80: true, 110: true, 135: true, 139: true,
	143: true, 443: true, 445: true, 465: true, 587: true, 993: true, 995: true,
	3306: true, 3389: true, 5432: true, 5900: true, 6379: true, 8080: true,
	8443: true, 9000: true, 9090: true, 9200: true, 11211: true, 27017: true,
}

// PortState 记录最近解析出的实际端口，供下次启动沿用，避免端口漂移。
type PortState struct {
	SyncplayPort int `json:"syncplayPort"`
	WebPort      int `json:"webPort"`
}

// PortAvailable 用临时 bind 探测端口是否空闲（探测后立即关闭让出）。
// 双地址族探测：分别尝试 IPv4（0.0.0.0:port）与 IPv6（[::]:port）通配绑定，
// 任一失败即视为端口不可用。原因：":port" 在 Go 中默认解析为 IPv6 通配
// [::]:port，在 v6only 平台（Windows/macOS）感知不到仅绑 IPv4
// （如 127.0.0.1:port）的占用者，会误判空闲；双地址族覆盖所有平台——
// v6only 平台上 IPv4 占用撞 0.0.0.0 探测、IPv6 占用撞 [::] 探测，
// Linux dual-stack 上两者本来就互相冲突，同样正确检测。
// 系统未启用某地址族（如 IPv6 被禁用）时，该族探测必然失败，
// 先用端口 0 基线确认地址族本身不可用，则跳过、仅以另一族结果为准。
func PortAvailable(port int) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	// IPv4 通配探测：正常系统必然支持 IPv4，失败即端口被 IPv4 占用
	if !probeBind(fmt.Sprintf("0.0.0.0:%d", port)) {
		return false
	}
	// IPv6 通配探测：地址族可用（非禁 IPv6 环境）却 bind 失败 → 端口被占用
	if !probeBind(fmt.Sprintf("[::]:%d", port)) && probeBind("[::]:0") {
		return false
	}
	return true
}

// probeBind 尝试在 addr 上 bind 后立即关闭，成功返回 true（不改变监听状态）。
func probeBind(addr string) bool {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// ResolvePort 决定实际使用的端口，返回 (端口, 是否避让)。
//
// 策略：
//   - configured > 0：用户显式设定 → 优先使用，被占则随机避让
//   - configured < 0：非法配置 → 原样返回（让真正的监听报错，如测试用 -1）
//   - configured == 0：未设定 → 尝试上次记住的端口（无则内置首选），被占则随机避让
//
// 随机避让区间 1024-65535，排除常见服务端口表与系统已占用端口。
func ResolvePort(configured, preferred, remembered int) (int, bool) {
	if configured < 0 {
		return configured, false
	}
	candidate := preferred
	if configured > 0 {
		candidate = configured
	} else if remembered > 0 {
		candidate = remembered
	}
	if candidate > 0 && PortAvailable(candidate) {
		return candidate, false
	}
	return randomAvoidPort(), true
}

// randomAvoidPort 在 1024-65535 内随机选一个空闲且不在常见服务端口表内的端口。
// 极端环境下（候选端口几乎全被占用）有界重试后返回最后一个候选，交由真实监听报错。
func randomAvoidPort() int {
	const minPort = 1024
	const maxPort = 65535
	candidate := minPort
	for attempt := 0; attempt < 5000; attempt++ {
		p := minPort + rand.IntN(maxPort-minPort+1)
		if commonServicePorts[p] {
			continue
		}
		if PortAvailable(p) {
			return p
		}
		candidate = p
	}
	return candidate
}

// DataDir 返回数据目录（端口记忆等状态文件的存放处）。
// 优先使用环境变量 SYNCMEDIA_DATA_DIR；否则用可执行文件所在目录下的 data/。
// 选择理由：安装后二进制位于 ~/syncmedia/ 下 → 状态落在 ~/syncmedia/data/，
// 随安装目录走，升级/迁移不丢；多安装实例互不干扰；无需写 /var 等特权目录，
// 也不依赖 $HOME（Android/Termux 等环境可能没有稳定 HOME）。
func DataDir() (string, error) {
	if d := os.Getenv("SYNCMEDIA_DATA_DIR"); d != "" {
		return d, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "data"), nil
}

// portsStateFile 返回端口状态文件路径（data/ports.json）。
func portsStateFile(dir string) string {
	return filepath.Join(dir, "ports.json")
}

// LoadPortState 读取端口状态文件；文件不存在时返回零值（首次运行）。
func LoadPortState(dir string) (PortState, error) {
	data, err := os.ReadFile(portsStateFile(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return PortState{}, nil
		}
		return PortState{}, err
	}
	var st PortState
	if err := json.Unmarshal(data, &st); err != nil {
		return PortState{}, err
	}
	return st, nil
}

// SavePortState 写入端口状态文件（自动创建目录）。
func SavePortState(dir string, st PortState) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(portsStateFile(dir), data, 0o644)
}
