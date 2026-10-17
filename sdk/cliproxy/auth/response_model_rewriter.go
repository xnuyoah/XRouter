package auth

import (
	"bytes"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var modelFieldPaths = []string{"model", "modelVersion", "response.model", "response.modelVersion", "message.model"}

const maxPendingBufSize = 1 << 20 // 待处理缓冲区上限 1MB

func rewriteSSEPayloadLines(payload []byte, targetModel string) []byte {
	if targetModel == "" || len(payload) == 0 {
		return payload
	}
	lines := bytes.Split(payload, []byte("\n"))
	out := make([][]byte, 0, len(lines))
	for _, line := range lines {
		prefix, jsonData, ok := extractSSEDataLine(line)
		if ok && len(jsonData) > 0 && jsonData[0] == '{' && gjson.ValidBytes(jsonData) {
			rewritten := rewriteModelInResponse(jsonData, targetModel)
			line = append(append([]byte{}, prefix...), rewritten...)
		}
		out = append(out, line)
	}
	joined := bytes.Join(out, []byte("\n"))
	if len(payload) > 0 && payload[len(payload)-1] == '\n' && (len(joined) == 0 || joined[len(joined)-1] != '\n') {
		joined = append(joined, '\n')
	}
	return joined
}

func rewriteModelInResponse(data []byte, targetModel string) []byte {
	if targetModel == "" || len(data) == 0 {
		return data
	}
	for _, path := range modelFieldPaths {
		if gjson.GetBytes(data, path).Exists() {
			data, _ = sjson.SetBytes(data, path, targetModel)
		}
	}
	return data
}

// StreamRewriteOptions 配置流式重写器。
type StreamRewriteOptions struct {
	RewriteModel string
}

// StreamRewriter 重写流式 SSE 响应中的模型名。
type StreamRewriter struct {
	options         StreamRewriteOptions
	pendingBuf      []byte
	loggedPaths     map[string]bool
	rewrittenChunks int
	loggedFinished  bool
}

// NewStreamRewriter 创建一个新的流式重写器。
func NewStreamRewriter(options StreamRewriteOptions) *StreamRewriter {
	return &StreamRewriter{
		options:     options,
		pendingBuf:  nil,
		loggedPaths: make(map[string]bool),
	}
}

func (r *StreamRewriter) rewriteModel(data []byte) []byte {
	if r == nil || r.options.RewriteModel == "" || len(data) == 0 {
		return data
	}
	targetModel := r.options.RewriteModel
	rewroteAny := false
	for _, path := range modelFieldPaths {
		if gjson.GetBytes(data, path).Exists() {
			data, _ = sjson.SetBytes(data, path, targetModel)
			rewroteAny = true
			if r.loggedPaths == nil {
				r.loggedPaths = make(map[string]bool)
			}
			if !r.loggedPaths[path] {
				r.loggedPaths[path] = true
				log.Debugf("response rewriter: stream started, rewrote model at path %s to %s", path, targetModel)
			}
		}
	}
	if rewroteAny {
		r.rewrittenChunks++
	}
	return data
}

func (r *StreamRewriter) rewriteSSELines(payload []byte) []byte {
	if r == nil || r.options.RewriteModel == "" || len(payload) == 0 {
		return payload
	}
	lines := bytes.Split(payload, []byte("\n"))
	out := make([][]byte, 0, len(lines))
	for _, line := range lines {
		prefix, jsonData, ok := extractSSEDataLine(line)
		if ok && len(jsonData) > 0 && jsonData[0] == '{' && gjson.ValidBytes(jsonData) {
			rewritten := r.rewriteModel(jsonData)
			line = append(append([]byte{}, prefix...), rewritten...)
		}
		out = append(out, line)
	}
	joined := bytes.Join(out, []byte("\n"))
	if len(payload) > 0 && payload[len(payload)-1] == '\n' && (len(joined) == 0 || joined[len(joined)-1] != '\n') {
		joined = append(joined, '\n')
	}
	return joined
}

func (r *StreamRewriter) logStreamFinishedOnce() {
	if r == nil || r.loggedFinished || r.rewrittenChunks == 0 {
		return
	}
	r.loggedFinished = true
	log.Debugf("response rewriter: stream finished, successfully rewrote %d chunks to %s", r.rewrittenChunks, r.options.RewriteModel)
}

// RewriteChunk 重写单个 SSE 分块中的模型名。
func (r *StreamRewriter) RewriteChunk(chunk []byte) []byte {
	if r.options.RewriteModel == "" {
		return chunk
	}

	if len(r.pendingBuf) > 0 {
		combined := make([]byte, 0, len(r.pendingBuf)+1+len(chunk))
		combined = append(combined, r.pendingBuf...)
		if combined[len(combined)-1] != '\n' {
			combined = append(combined, '\n')
		}
		combined = append(combined, chunk...)
		chunk = combined
		r.pendingBuf = nil
	}
	chunk = normalizeGluedSSEEvents(chunk)

	if len(chunk) > maxPendingBufSize {
		return chunk
	}

	// 处理原始 JSON 分块（不带 SSE "data:" 前缀的 OpenAI 格式）
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) > 0 && trimmed[0] == '{' && gjson.ValidBytes(trimmed) {
		rewritten := trimmed
		if r.options.RewriteModel != "" {
			rewritten = r.rewriteModel(rewritten)
		}
		return rewritten
	}

	lastDoubleNewline := bytes.LastIndex(chunk, []byte("\n\n"))

	var processChunk []byte
	if lastDoubleNewline >= 0 {
		afterComplete := chunk[lastDoubleNewline+2:]
		if len(afterComplete) > 0 && !bytes.Equal(afterComplete, []byte("\n")) {
			processChunk = chunk[:lastDoubleNewline+2]
			r.pendingBuf = make([]byte, len(afterComplete))
			copy(r.pendingBuf, afterComplete)
		} else {
			processChunk = chunk
		}
	} else if gjson.ValidBytes(extractLastDataPayload(chunk)) {
		processChunk = chunk
	} else if len(bytes.TrimSpace(chunk)) == 0 {
		return chunk
	} else if len(chunk) > 0 {
		r.pendingBuf = make([]byte, len(chunk))
		copy(r.pendingBuf, chunk)
		return nil
	} else {
		return chunk
	}

	lines := bytes.Split(processChunk, []byte("\n"))
	var result [][]byte
	var pendingEvent []byte
	skipBlanks := false

	for _, line := range lines {
		if len(line) == 0 && skipBlanks {
			continue
		}
		if len(line) != 0 && skipBlanks {
			skipBlanks = false
		}

		if bytes.HasPrefix(line, []byte("event:")) {
			pendingEvent = line
			continue
		}

		dataPrefix, jsonData, found := extractSSEDataLine(line)
		if found && len(jsonData) > 0 && jsonData[0] == '{' {
			if !gjson.ValidBytes(jsonData) {
				if pendingEvent != nil {
					r.pendingBuf = append(pendingEvent, '\n')
					r.pendingBuf = append(r.pendingBuf, line...)
					pendingEvent = nil
				} else {
					r.pendingBuf = append(r.pendingBuf, line...)
				}
				continue
			}

			if pendingEvent != nil {
				result = append(result, pendingEvent)
				pendingEvent = nil
			}

			rewritten := jsonData
			if r.options.RewriteModel != "" {
				rewritten = r.rewriteModel(jsonData)
			}
			result = append(result, append(dataPrefix, rewritten...))
			continue
		}

		if pendingEvent != nil {
			result = append(result, pendingEvent)
			pendingEvent = nil
		}
		result = append(result, line)
	}

	if pendingEvent != nil {
		result = append(result, pendingEvent)
	}

	joined := bytes.Join(result, []byte("\n"))
	if len(joined) == 0 && len(chunk) > 0 {
		return r.rewriteSSELines(chunk)
	}
	if bytes.Contains(chunk, []byte("[DONE]")) {
		r.logStreamFinishedOnce()
	}
	return joined
}

func extractLastDataPayload(chunk []byte) []byte {
	lines := bytes.Split(chunk, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if _, jsonData, found := extractSSEDataLine(lines[i]); found && len(jsonData) > 0 {
			return jsonData
		}
	}
	return nil
}

func extractSSEDataLine(line []byte) (prefix []byte, jsonData []byte, ok bool) {
	if jsonData, found := bytes.CutPrefix(line, []byte("data: ")); found {
		return []byte("data: "), jsonData, true
	}
	if jsonData, found := bytes.CutPrefix(line, []byte("data:")); found {
		return []byte("data:"), jsonData, true
	}
	return nil, nil, false
}

func normalizeGluedSSEEvents(chunk []byte) []byte {
	if len(chunk) == 0 {
		return chunk
	}
	// Antigravity 转换器发出的事件帧不带尾部空行。
	// 多个帧连续缓冲时可能粘连成 "...}event:..."。
	// 仅当粘连处之前的字节能闭合一个有效的 SSE data JSON 对象时才拆分。
	chunk = safeReplaceGlued(chunk, []byte("}event:"), []byte("}\n\nevent:"))
	chunk = safeReplaceGlued(chunk, []byte("}\r\nevent:"), []byte("}\r\n\r\nevent:"))
	// Codex 执行器每行 SSE 发出一个 "data: {json}" 分块且不带尾部换行。
	// 缓冲的分块可能粘连成 "...}data:..."。
	chunk = safeReplaceGlued(chunk, []byte("}data:"), []byte("}\ndata:"))
	chunk = safeReplaceGlued(chunk, []byte("}\r\ndata:"), []byte("}\r\ndata:"))
	return chunk
}

func safeReplaceGlued(chunk []byte, old, new []byte) []byte {
	if len(old) == 0 || len(chunk) == 0 {
		return chunk
	}
	if !bytes.Contains(chunk, old) {
		return chunk
	}
	var result []byte
	remaining := chunk
	for {
		idx := bytes.Index(remaining, old)
		if idx == -1 {
			result = append(result, remaining...)
			break
		}
		lineStart := bytes.LastIndexByte(remaining[:idx], '\n')
		var part []byte
		if lineStart == -1 {
			part = remaining[:idx+1]
		} else {
			part = remaining[lineStart+1 : idx+1]
		}
		_, jsonData, ok := extractSSEDataLine(part)
		if ok && len(jsonData) > 0 && gjson.ValidBytes(jsonData) {
			result = append(result, remaining[:idx]...)
			result = append(result, new...)
			remaining = remaining[idx+len(old):]
			continue
		}
		result = append(result, remaining[:idx+len(old)]...)
		remaining = remaining[idx+len(old):]
	}
	return result
}

// Finish 在流结束时冲刷所有缓冲的不完整 SSE 数据。
func (r *StreamRewriter) Finish() []byte {
	if len(r.pendingBuf) == 0 {
		return nil
	}
	buf := make([]byte, len(r.pendingBuf)+2)
	copy(buf, r.pendingBuf)
	buf[len(r.pendingBuf)] = '\n'
	buf[len(r.pendingBuf)+1] = '\n'
	buf = normalizeGluedSSEEvents(buf)
	r.pendingBuf = nil
	out := r.RewriteChunk(buf)
	if len(r.pendingBuf) > 0 {
		tail := r.rewriteSSELines(r.pendingBuf)
		r.pendingBuf = nil
		if len(tail) > 0 {
			if len(out) > 0 {
				out = append(out, tail...)
			} else {
				out = tail
			}
		}
	}
	r.logStreamFinishedOnce()
	return out
}
