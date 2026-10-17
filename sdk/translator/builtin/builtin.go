// Package builtin 向 SDK 使用者暴露内置转换器的注册项。
package builtin

import (
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"

	_ "github.com/xnuyoah/XRouter/v8/internal/translator"
)

// Registry 暴露已填充所有内置转换器的默认注册表。
func Registry() *sdktranslator.Registry {
	return sdktranslator.Default()
}

// Pipeline 返回一个已经包含内置转换器的流水线。
func Pipeline() *sdktranslator.Pipeline {
	return sdktranslator.NewPipeline(sdktranslator.Default())
}
