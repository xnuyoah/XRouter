package util_test

import (
	"strings"
	"testing"

	"github.com/xnuyoah/XRouter/v8/internal/util"
)

// TestMaskSensitiveQueryMasksValue 覆盖 issue #6666 的另一半场景：
// 按值删除客户端 API key 时会以 ?value= 传递密钥，因此
// 请求日志记录器必须像其他携带密钥的参数一样对其打码。
func TestMaskSensitiveQueryMasksValue(t *testing.T) {
	secretKey := "sk-secret-client-key"

	t.Run("value parameter is masked", func(t *testing.T) {
		masked := util.MaskSensitiveQuery("value=" + secretKey)
		if strings.Contains(masked, secretKey) {
			t.Fatalf("value parameter leaked in full: %s", masked)
		}
		if masked != "value=sk-s...-key" {
			t.Fatalf("masked=%q want the HideAPIKey form", masked)
		}
	})

	t.Run("index parameter is untouched", func(t *testing.T) {
		masked := util.MaskSensitiveQuery("index=3")
		if masked != "index=3" {
			t.Fatalf("masked=%q want %q", masked, "index=3")
		}
	})

	t.Run("known secret parameters stay masked", func(t *testing.T) {
		masked := util.MaskSensitiveQuery("key=" + secretKey + "&access_token=" + secretKey)
		if strings.Contains(masked, secretKey) {
			t.Fatalf("secret parameter leaked in full: %s", masked)
		}
	})
}
