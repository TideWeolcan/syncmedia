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
