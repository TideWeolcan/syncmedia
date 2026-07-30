package manager

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TideWeolcan/syncmedia/internal/config"
)

// SettingsResponse 返回当前可配置项。
type SettingsResponse struct {
	TunnelType    string `json:"tunnelType"`
	ProxyURL      string `json:"proxyUrl"`
	BindInterface string `json:"bindInterface"`
	NoProxy       bool   `json:"noProxy"`
	TLSEnabled    bool   `json:"tlsEnabled"`
}

// SettingsRequest 接收设置更新。
type SettingsRequest struct {
	TunnelType    string `json:"tunnelType"`
	ProxyURL      string `json:"proxyUrl"`
	BindInterface string `json:"bindInterface"`
	NoProxy       *bool  `json:"noProxy"`
}

// WebServer 嵌入式 Web UI。
type WebServer struct {
	port   int
	mgr    *Manager
	logger *log.Logger
	server *http.Server
}

func NewWebServer(cfg config.WebConfig, mgr *Manager, logger *log.Logger) (*WebServer, error) {
	bind := cfg.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	if !isLoopbackBind(bind) && cfg.Token == "" {
		return nil, fmt.Errorf("web 绑定地址 %q 非本机回环，必须配置 web token（config web.token 或环境变量 SYNCMEDIA_WEB_TOKEN）", bind)
	}
	ws := &WebServer{port: cfg.Port, mgr: mgr, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("/", ws.handleIndex)
	mux.HandleFunc("/api/status", ws.handleStatus)
	mux.HandleFunc("/api/settings", ws.handleSettings)
	mux.HandleFunc("/api/restart", ws.handleRestart)
	var handler http.Handler = mux
	if cfg.Token != "" {
		handler = requireToken(cfg.Token, mux)
	}
	ws.server = &http.Server{
		Addr:              net.JoinHostPort(bind, strconv.Itoa(cfg.Port)),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return ws, nil
}

// isLoopbackBind 判定绑定地址是否为本机回环："localhost" 或可解析且为
// loopback 的 IP 视为本机；其余（0.0.0.0、::、外网 IP、任意主机名）视为非本机。
func isLoopbackBind(bind string) bool {
	if bind == "localhost" {
		return true
	}
	ip := net.ParseIP(bind)
	return ip != nil && ip.IsLoopback()
}

// requireToken 包住全部路由：Basic auth 的 password == token（用户名忽略，
// 浏览器原生凭据弹窗可用）或 Authorization: Bearer <token> 均放行；
// 比较使用恒定时间，避免时序侧信道。
func requireToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pass, ok := r.BasicAuth(); ok &&
			subtle.ConstantTimeCompare([]byte(pass), []byte(token)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") &&
			subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, "Bearer ")), []byte(token)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="syncmedia"`)
		http.Error(w, "未认证", http.StatusUnauthorized)
	})
}

func (ws *WebServer) Start() error { return ws.server.ListenAndServe() }
func (ws *WebServer) Stop()        { if ws.server != nil { ws.server.Close() } }

func (ws *WebServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ws.mgr.Status())
}

func (ws *WebServer) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ws.mgr.GetSettings())
		return
	}
	if r.Method == "POST" {
		var req SettingsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "无效请求", 400)
			return
		}
		ws.mgr.UpdateSettings(req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "已保存，正在重启..."})
		return
	}
	http.Error(w, "方法不允许", 405)
}

func (ws *WebServer) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "方法不允许", 405)
		return
	}
	go ws.mgr.Restart()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "正在重启..."})
}

func (ws *WebServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(indexHTML))
}

const indexHTML = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>SyncMedia</title>
<style>
:root { --bg:#0a0f1e; --card:rgba(30,41,59,0.7); --border:rgba(56,189,248,0.15); --accent:#38bdf8; --accent2:#818cf8; --green:#4ade80; --text:#e2e8f0; --dim:#64748b; }
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;background:var(--bg);color:var(--text);min-height:100vh;
background-image:radial-gradient(ellipse at top,rgba(56,189,248,0.08),transparent 50%),radial-gradient(ellipse at bottom,rgba(129,140,248,0.06),transparent 50%)}
.wrap{max-width:520px;margin:0 auto;padding:24px 16px 48px}
.header{text-align:center;padding:32px 0 24px}
.header h1{font-size:1.75rem;font-weight:700;background:linear-gradient(135deg,var(--accent),var(--accent2));-webkit-background-clip:text;-webkit-text-fill-color:transparent;margin-bottom:6px}
.header p{color:var(--dim);font-size:.85rem}
.card{background:var(--card);border:1px solid var(--border);border-radius:16px;padding:20px;margin-bottom:16px;backdrop-filter:blur(12px)}
.card-title{font-size:.75rem;font-weight:600;text-transform:uppercase;letter-spacing:.08em;color:var(--accent);margin-bottom:16px}
.addr-box{background:rgba(0,0,0,0.3);border-radius:12px;padding:20px;text-align:center;margin-bottom:12px;position:relative;cursor:pointer;transition:border .2s}
.addr-box:hover{border:1px solid var(--green)}
.addr-text{font-size:1.4rem;font-family:"SF Mono",monospace;font-weight:700;color:var(--green);word-break:break-all}
.addr-hint{color:var(--dim);font-size:.75rem;margin-top:6px}
.copy-btn{position:absolute;top:12px;right:12px;background:rgba(255,255,255,0.08);border:none;border-radius:8px;padding:6px 12px;color:var(--accent);cursor:pointer;font-size:.75rem;transition:all .2s}
.copy-btn:hover{background:rgba(56,189,248,0.2)}
.stats{display:grid;grid-template-columns:1fr 1fr;gap:12px}
.stat{background:rgba(0,0,0,0.2);border-radius:10px;padding:14px;text-align:center}
.stat-label{color:var(--dim);font-size:.7rem;text-transform:uppercase;letter-spacing:.05em;margin-bottom:4px}
.stat-value{font-size:1.1rem;font-weight:600;color:var(--text)}
.stat-value.on{color:var(--green)}
.field{margin-bottom:16px}
.field label{display:block;font-size:.8rem;color:var(--dim);margin-bottom:6px}
.field input,.field select{width:100%;background:rgba(0,0,0,0.3);border:1px solid var(--border);border-radius:10px;padding:12px 14px;color:var(--text);font-size:.9rem;outline:none;transition:border .2s}
.field input:focus,.field select:focus{border-color:var(--accent)}
.toggle-row{display:flex;align-items:center;justify-content:space-between;padding:12px 0}
.toggle-row span{font-size:.85rem;color:var(--text)}
.switch{position:relative;width:44px;height:24px;background:rgba(255,255,255,0.1);border-radius:12px;cursor:pointer;transition:background .3s}
.switch.on{background:var(--accent)}
.switch::after{content:"";position:absolute;top:2px;left:2px;width:20px;height:20px;background:#fff;border-radius:50%;transition:transform .3s}
.switch.on::after{transform:translateX(20px)}
.btn{width:100%;padding:14px;border:none;border-radius:12px;font-size:.95rem;font-weight:600;cursor:pointer;transition:all .2s}
.btn-primary{background:linear-gradient(135deg,var(--accent),var(--accent2));color:#fff}
.btn-primary:hover{opacity:.9;transform:translateY(-1px)}
.btn-danger{background:rgba(248,113,113,0.15);color:#f87171;border:1px solid rgba(248,113,113,0.3)}
.btn-danger:hover{background:rgba(248,113,113,0.25)}
.loading{display:flex;align-items:center;justify-content:center;padding:40px;color:var(--dim)}
.loading .dot{width:8px;height:8px;background:var(--accent);border-radius:50%;margin:0 3px;animation:bounce 1.4s infinite}
.loading .dot:nth-child(2){animation-delay:.2s}
.loading .dot:nth-child(3){animation-delay:.4s}
@keyframes bounce{0%,60%,100%{transform:translateY(0)}30%{transform:translateY(-8px)}}
.toast{position:fixed;bottom:24px;left:50%;transform:translateX(-50%) translateY(100px);background:var(--card);border:1px solid var(--green);border-radius:12px;padding:12px 24px;color:var(--green);font-size:.85rem;transition:transform .3s;z-index:99;backdrop-filter:blur(12px)}
.toast.show{transform:translateX(-50%) translateY(0)}
</style>
</head>
<body>
<div class="wrap">
<div class="header"><h1>SyncMedia</h1><p>syncplay + bore/frp 同步观影服务器</p></div>
<div id="app"><div class="loading"><div class="dot"></div><div class="dot"></div><div class="dot"></div></div></div>
</div>
<div class="toast" id="toast"></div>

<script>
let currentSettings = {};
async function api(path, opts) {
  const r = await fetch(path, opts);
  return r.json();
}
function toast(msg) {
  const t = document.getElementById('toast');
  t.textContent = msg; t.classList.add('show');
  setTimeout(() => t.classList.remove('show'), 2500);
}
async function copyAddr(text) {
  try { await navigator.clipboard.writeText(text); toast('已复制到剪贴板'); }
  catch(e) { toast('复制失败'); }
}
async function loadStatus() {
  try {
    const s = await api('/api/status');
    const settings = await api('/api/settings');
    currentSettings = settings;
    renderApp(s, settings);
  } catch(e) {
    document.getElementById('app').innerHTML = '<div class="card"><p style="color:#f87171;text-align:center">无法连接服务</p></div>';
  }
}
function renderApp(s, settings) {
  const addr = s.publicAddr || ('127.0.0.1:' + s.serverPort);
  const isPublic = !!s.publicAddr;
  document.getElementById('app').innerHTML = '\
    <div class="card">\
      <div class="card-title">公共地址</div>\
      <div class="addr-box" onclick="copyAddr(\''+addr+'\')">\
        <button class="copy-btn" onclick="event.stopPropagation();copyAddr(\''+addr+'\')">复制</button>\
        <div class="addr-text">'+addr+'</div>\
        <div class="addr-hint">'+(isPublic ? '在 Kazumi 自定义服务器中填入此地址' : '隧道未建立，仅本地可用')+'</div>\
      </div>\
      <div class="stats">\
        <div class="stat"><div class="stat-label">隧道</div><div class="stat-value">'+(s.tunnelType||'无')+'</div></div>\
        <div class="stat"><div class="stat-label">TLS</div><div class="stat-value '+(s.tlsEnabled?'on':'')+'">'+(s.tlsEnabled?'TLS 1.3':'关闭')+'</div></div>\
        <div class="stat"><div class="stat-label">国密</div><div class="stat-value '+(s.tlsEnabled?'on':'')+'">'+(s.tlsEnabled?'SM2/SM3/SM4':'关闭')+'</div></div>\
        <div class="stat"><div class="stat-label">运行时间</div><div class="stat-value">'+s.uptime+'</div></div>\
      </div>\
    </div>\
    <div class="card">\
      <div class="card-title">网络设置</div>\
      <div class="field">\
        <label>隧道类型</label>\
        <select id="tunnelType">\
          <option value="bore"'+(settings.tunnelType==='bore'?' selected':'')+'>bore (bore.pub)</option>\
          <option value="frp"'+(settings.tunnelType==='frp'?' selected':'')+'>frp</option>\
          <option value="none"'+(settings.tunnelType==='none'?' selected':'')+'>仅本地</option>\
        </select>\
      </div>\
      <div class="field">\
        <label>网络代理 (HTTP/SOCKS5)</label>\
        <input type="text" id="proxyUrl" placeholder="如 socks5://127.0.0.1:1080" value="'+(settings.proxyUrl||'')+'">\
      </div>\
      <div class="field">\
        <label>绑定网卡（绕过 VPN）</label>\
        <input type="text" id="bindInterface" placeholder="如 wlan0（留空=不绑定）" value="'+(settings.bindInterface||'')+'">\
      </div>\
      <div class="toggle-row">\
        <span>绕过系统代理</span>\
        <div class="switch '+(settings.noProxy?'on':'')+'" id="noProxySwitch" onclick="this.classList.toggle(\'on\')"></div>\
      </div>\
      <button class="btn btn-primary" onclick="saveSettings()" style="margin-top:8px">保存并重启</button>\
    </div>\
    <div class="card">\
      <div class="card-title">服务控制</div>\
      <button class="btn btn-danger" onclick="restartService()">重启服务</button>\
    </div>';
}
async function saveSettings() {
  const data = {
    tunnelType: document.getElementById('tunnelType').value,
    proxyUrl: document.getElementById('proxyUrl').value,
    bindInterface: document.getElementById('bindInterface').value,
    noProxy: document.getElementById('noProxySwitch').classList.contains('on'),
  };
  toast('保存中...');
  await api('/api/settings', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(data)});
  toast('已保存，正在重启...');
  setTimeout(() => { document.getElementById('app').innerHTML = '<div class="loading"><div class="dot"></div><div class="dot"></div><div class="dot"></div></div>'; loadStatus(); }, 3000);
}
async function restartService() {
  toast('重启中...');
  await api('/api/restart', {method:'POST'});
  setTimeout(() => { document.getElementById('app').innerHTML = '<div class="loading"><div class="dot"></div><div class="dot"></div><div class="dot"></div></div>'; loadStatus(); }, 3000);
}
loadStatus();
setInterval(loadStatus, 5000);
</script>
</body>
</html>`
