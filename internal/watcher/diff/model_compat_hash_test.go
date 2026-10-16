package diff

import (
	"testing"

	"github.com/xnuyoah/XRouter/v8/internal/config"
)

func TestModelHashesIncludeIsCompat(t *testing.T) {
	if ComputeNativeAPIModelsHash([]config.NativeAPIModel{{Name: "m"}}) == ComputeNativeAPIModelsHash([]config.NativeAPIModel{{Name: "m", IsCompat: true}}) {
		t.Fatal("NativeAPI model hash did not change when IsCompat changed")
	}
	if ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m"}}) == ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m", IsCompat: true}}) {
		t.Fatal("OpenAI compatibility model hash did not change when IsCompat changed")
	}
}
