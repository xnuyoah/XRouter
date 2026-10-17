package responses

import (
	"os"
	"testing"
)

// TestMain 按 tool_search 测试的预期，在开启 upstream.claude.tool-search
// 的情况下运行该包；覆盖关闭状态的测试会按用例单独切换。
func TestMain(m *testing.M) {
	SetClientToolSearchEnabled(true)
	os.Exit(m.Run())
}

func setClientToolSearchEnabledForTest(t *testing.T, enabled bool) {
	t.Helper()
	previous := ClientToolSearchEnabled()
	SetClientToolSearchEnabled(enabled)
	t.Cleanup(func() { SetClientToolSearchEnabled(previous) })
}
