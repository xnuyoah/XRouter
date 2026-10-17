package htmlsanitize

import (
	"bytes"
	"encoding/json"
	"html"
	"io"
	"mime"
	"strings"
)

// String 在文本返回给面向浏览器的管理客户端之前对其进行转义。
func String(value string) string {
	return html.EscapeString(value)
}

// Strings 转义 values 中的每个字符串，同时保持顺序。
func Strings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, String(value))
	}
	return out
}

// JSONBody 转义 JSON 文档中的所有字符串值。
func JSONBody(body []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return body, false
	}

	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return body, false
	}
	var extra any
	if errExtra := decoder.Decode(&extra); errExtra != io.EOF {
		return body, false
	}

	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if errEncode := encoder.Encode(JSONValue(value)); errEncode != nil {
		return body, false
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), true
}

// JSONBodyIfLikely 在内容类型或正文形态表明为 JSON 时转义 JSON 正文。
func JSONBodyIfLikely(body []byte, contentType string) ([]byte, bool) {
	if IsJSONContentType(contentType) || LooksLikeJSON(body) {
		return JSONBody(body)
	}
	return body, false
}

// JSONValue 递归转义 JSON 兼容数据中的字符串值。
func JSONValue(value any) any {
	switch typed := value.(type) {
	case string:
		return String(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = JSONValue(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = JSONValue(item)
		}
		return out
	default:
		return value
	}
}

// IsJSONContentType 报告 contentType 是否为 application/json 或 +json 类型。
func IsJSONContentType(contentType string) bool {
	mediaType, _, errParse := mime.ParseMediaType(strings.TrimSpace(contentType))
	if errParse != nil {
		mediaType = strings.TrimSpace(contentType)
	}
	mediaType = strings.ToLower(mediaType)
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// LooksLikeJSON 报告 body 是否以对象或数组的 JSON 标记开头。
func LooksLikeJSON(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return false
	}
	return trimmed[0] == '{' || trimmed[0] == '['
}
