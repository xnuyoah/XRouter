package translator

import (
	_ "github.com/xnuyoah/XRouter/v8/internal/translator/claude/openai/chat-completions"
	_ "github.com/xnuyoah/XRouter/v8/internal/translator/claude/openai/responses"

	_ "github.com/xnuyoah/XRouter/v8/internal/translator/codex/claude"
	_ "github.com/xnuyoah/XRouter/v8/internal/translator/codex/openai/chat-completions"
	_ "github.com/xnuyoah/XRouter/v8/internal/translator/codex/openai/responses"

	_ "github.com/xnuyoah/XRouter/v8/internal/translator/openai/claude"
	_ "github.com/xnuyoah/XRouter/v8/internal/translator/openai/openai/chat-completions"
	_ "github.com/xnuyoah/XRouter/v8/internal/translator/openai/openai/responses"
)
