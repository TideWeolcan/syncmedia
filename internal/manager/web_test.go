package manager

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/TideWeolcan/syncmedia/internal/config"
)

func TestIndexHandlerInlineScriptParses(t *testing.T) {
	ws, err := NewWebServer(config.WebConfig{}, nil, nil)
	if err != nil {
		t.Fatalf("NewWebServer: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host = "127.0.0.1" // 回环绑定的 Host 校验白名单
	response := httptest.NewRecorder()
	ws.server.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}

	scriptPattern := regexp.MustCompile(`(?is)<script(?:\s[^>]*)?>(.*?)</script\s*>`)
	scripts := scriptPattern.FindAllStringSubmatch(response.Body.String(), -1)
	if len(scripts) != 1 {
		t.Fatalf("GET / inline <script> count = %d, want 1", len(scripts))
	}

	script := scripts[0][1]
	if strings.TrimSpace(script) == "" {
		t.Fatal("GET / inline <script> is empty")
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node is required for JavaScript syntax checking: %v", err)
	}

	cmd := exec.Command(node, "--check", "-")
	cmd.Stdin = strings.NewReader(script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("GET / inline <script> failed %s --check: %v\n%s", node, err, output)
	}
}

// TestIndexEscapesUserInput：indexHTML 必须对用户可控字段（proxyUrl /
// bindInterface）在渲染进 value 属性前做 HTML 转义，防止含引号的输入
// 突破属性注入事件处理器（存储型 XSS）。
func TestIndexEscapesUserInput(t *testing.T) {
	for _, needle := range []string{
		"esc(settings.proxyUrl",
		"esc(settings.bindInterface",
	} {
		if !strings.Contains(indexHTML, needle) {
			t.Errorf("indexHTML 缺少对 %s 的转义处理", needle)
		}
	}
	// esc 函数本身必须存在且转义双引号
	if !strings.Contains(indexHTML, `replace(/"/g,'&quot;')`) {
		t.Error("indexHTML 的 esc() 未转义双引号")
	}
}
