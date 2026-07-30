package main

import (
	"bytes"
	"regexp"
	"testing"
)

// TestUsageListsAllFlags：usage 的选项列表必须覆盖 runStart 定义的全部
// 10 个 flag。用行首锚定匹配独立选项行，避免 --no-proxy 行被误认为
// 已覆盖 --proxy。
func TestUsageListsAllFlags(t *testing.T) {
	var buf bytes.Buffer
	writeUsage(&buf)
	out := buf.String()

	flags := []string{
		"config", "port", "web-port", "no-tls", "tunnel",
		"bore-binary", "frp-server", "proxy", "bind-interface", "no-proxy",
	}
	for _, name := range flags {
		re := regexp.MustCompile(`(?m)^\s+--` + regexp.QuoteMeta(name) + `(\s|$)`)
		if !re.MatchString(out) {
			t.Errorf("usage 缺少选项行 --%s", name)
		}
	}
}
