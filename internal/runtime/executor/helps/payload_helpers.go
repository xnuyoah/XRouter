package helps

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

// ApplyPayloadConfigWithRoot 的行为类似 applyPayloadConfig，但它把所有参数
// 路径视为相对于所提供 root 路径，并在给出 protocol 时
// 将匹配限制为该协议。给出原始载荷时会针对它
// 检查默认值。requestedModel 携带别名解析之前
// 客户端可见的模型名，使载荷规则可以精确地针对别名。
// requestPath 是入站 HTTP 请求路径（若可用），用于端点作用域的门控。
func ApplyPayloadConfigWithRoot(cfg *config.Config, model, protocol, root string, payload, original []byte, requestedModel string, requestPath string) []byte {
	return ApplyPayloadConfigWithRequest(cfg, model, protocol, "", root, payload, original, requestedModel, requestPath, nil)
}

// ApplyPayloadConfigWithRequest 使用来源协议和请求头门控来应用载荷配置。
func ApplyPayloadConfigWithRequest(cfg *config.Config, model, protocol, fromProtocol, root string, payload, original []byte, requestedModel string, requestPath string, headers http.Header) []byte {
	out, _ := ApplyPayloadConfigWithRequestTracked(cfg, model, protocol, fromProtocol, root, payload, original, requestedModel, requestPath, headers, "")
	return out
}

// ApplyPayloadConfigWithRequestForExecutor 带着显式的目标执行器上下文应用载荷配置。
func ApplyPayloadConfigWithRequestForExecutor(cfg *config.Config, targetExecutor, model, protocol, fromProtocol, root string, payload, original []byte, requestedModel string, requestPath string, headers http.Header) []byte {
	out, _ := ApplyPayloadConfigWithTrackedPathsForExecutor(cfg, targetExecutor, model, protocol, fromProtocol, root, payload, original, requestedModel, requestPath, headers)
	return out
}

// ApplyPayloadConfigWithTrackedPaths 应用载荷配置，并报告哪些
// 被跟踪路径（或其后代）被某条已应用的规则命中。
func ApplyPayloadConfigWithTrackedPaths(cfg *config.Config, model, protocol, fromProtocol, root string, payload, original []byte, requestedModel string, requestPath string, headers http.Header, trackedPaths ...string) ([]byte, map[string]bool) {
	return ApplyPayloadConfigWithTrackedPathsForExecutor(cfg, "", model, protocol, fromProtocol, root, payload, original, requestedModel, requestPath, headers, trackedPaths...)
}

// ApplyPayloadConfigWithTrackedPathsForExecutor 带着目标执行器感知应用载荷配置。
// 当请求头表明是 Codex 客户端且目标执行器不是 Codex 或 Codex WebSocket 时，
// 它规范化工具参数的整数类型，以满足客户端的整数反序列化（#6237）。
// 对 Codex 和 Codex WebSocket 目标则跳过整数规范化（#6244）。
func ApplyPayloadConfigWithTrackedPathsForExecutor(cfg *config.Config, targetExecutor, model, protocol, fromProtocol, root string, payload, original []byte, requestedModel string, requestPath string, headers http.Header, trackedPaths ...string) ([]byte, map[string]bool) {
	touched := make(map[string]bool)
	if len(payload) == 0 {
		return payload, touched
	}
	if cfg == nil {
		return payload, touched
	}
	out := payload

	markTouched := func(resolvedPath string) {
		for _, tp := range trackedPaths {
			tp = strings.TrimSpace(tp)
			if tp != "" && payloadRuleTargetsPath(resolvedPath, tp) {
				touched[tp] = true
			}
		}
	}

	// 在载荷规则之前应用 disable-image-generation 过滤，这样配置载荷
	// 覆盖项在需要时可以显式重新启用 image_generation。
	if shouldStripImageGeneration(cfg.DisableImageGeneration, requestPath) {
		out = removeToolTypeFromPayloadWithRoot(out, root, "image_generation")
		out = removeToolChoiceFromPayloadWithRoot(out, root, "image_generation")
	}

	rules := cfg.Payload
	hasPayloadRules := len(rules.Default) != 0 || len(rules.DefaultRaw) != 0 || len(rules.Override) != 0 || len(rules.OverrideRaw) != 0 || len(rules.Filter) != 0
	if hasPayloadRules {
		model = strings.TrimSpace(model)
		requestedModel = strings.TrimSpace(requestedModel)
		if model != "" || requestedModel != "" {
			candidates := payloadModelCandidates(model, requestedModel)
			source := original
			if len(source) == 0 {
				source = payload
			}
			appliedDefaults := make(map[string]struct{})
			// 应用 default 规则：在所有匹配的规则中，每个字段以首次写入为准。
			for i := range rules.Default {
				rule := &rules.Default[i]
				if !payloadModelRulesMatch(rule.Models, protocol, fromProtocol, headers, out, root, candidates) {
					continue
				}
				for path, value := range rule.Params {
					fullPath := buildPayloadPath(root, path)
					if fullPath == "" {
						continue
					}
					for _, resolvedPath := range resolvePayloadRulePaths(out, fullPath) {
						if gjson.GetBytes(source, resolvedPath).Exists() {
							continue
						}
						if _, ok := appliedDefaults[resolvedPath]; ok {
							continue
						}
						updated, errSet := sjson.SetBytes(out, resolvedPath, value)
						if errSet != nil {
							continue
						}
						out = updated
						appliedDefaults[resolvedPath] = struct{}{}
						markTouched(resolvedPath)
					}
				}
			}
			// 应用 default raw 规则：在所有匹配的规则中，每个字段以首次写入为准。
			for i := range rules.DefaultRaw {
				rule := &rules.DefaultRaw[i]
				if !payloadModelRulesMatch(rule.Models, protocol, fromProtocol, headers, out, root, candidates) {
					continue
				}
				for path, value := range rule.Params {
					fullPath := buildPayloadPath(root, path)
					if fullPath == "" {
						continue
					}
					for _, resolvedPath := range resolvePayloadRulePaths(out, fullPath) {
						if gjson.GetBytes(source, resolvedPath).Exists() {
							continue
						}
						if _, ok := appliedDefaults[resolvedPath]; ok {
							continue
						}
						rawValue, ok := payloadRawValue(value)
						if !ok {
							continue
						}
						updated, errSet := sjson.SetRawBytes(out, resolvedPath, rawValue)
						if errSet != nil {
							continue
						}
						out = updated
						appliedDefaults[resolvedPath] = struct{}{}
						markTouched(resolvedPath)
					}
				}
			}
			// 应用 override 规则：在所有匹配的规则中，每个字段以最后写入为准。
			for i := range rules.Override {
				rule := &rules.Override[i]
				if !payloadModelRulesMatch(rule.Models, protocol, fromProtocol, headers, out, root, candidates) {
					continue
				}
				for path, value := range rule.Params {
					fullPath := buildPayloadPath(root, path)
					if fullPath == "" {
						continue
					}
					for _, resolvedPath := range resolvePayloadRulePaths(out, fullPath) {
						var applied bool
						out, applied = setPayloadValueIfDifferentTracked(out, resolvedPath, value)
						if applied {
							markTouched(resolvedPath)
						}
					}
				}
			}
			// 应用 override raw 规则：在所有匹配的规则中，每个字段以最后写入为准。
			for i := range rules.OverrideRaw {
				rule := &rules.OverrideRaw[i]
				if !payloadModelRulesMatch(rule.Models, protocol, fromProtocol, headers, out, root, candidates) {
					continue
				}
				for path, value := range rule.Params {
					fullPath := buildPayloadPath(root, path)
					if fullPath == "" {
						continue
					}
					rawValue, ok := payloadRawValue(value)
					if !ok {
						continue
					}
					for _, resolvedPath := range resolvePayloadRulePaths(out, fullPath) {
						var applied bool
						out, applied = setPayloadRawValueIfDifferentTracked(out, resolvedPath, rawValue)
						if applied {
							markTouched(resolvedPath)
						}
					}
				}
			}
			// 应用 filter 规则：从载荷中移除匹配的路径。
			for i := range rules.Filter {
				rule := &rules.Filter[i]
				if !payloadModelRulesMatch(rule.Models, protocol, fromProtocol, headers, out, root, candidates) {
					continue
				}
				for _, path := range rule.Params {
					fullPath := buildPayloadPath(root, path)
					if fullPath == "" {
						continue
					}
					resolvedPaths := resolvePayloadRulePaths(out, fullPath)
					for i := len(resolvedPaths) - 1; i >= 0; i-- {
						resolvedPath := resolvedPaths[i]
						updated, errDel := sjson.DeleteBytes(out, resolvedPath)
						if errDel != nil {
							continue
						}
						out = updated
						markTouched(resolvedPath)
					}
				}
			}
		}
	}
	return out, touched
}

// ApplyPayloadConfigWithRequestTracked 应用载荷配置，并报告是否
// 有某条已应用的规则命中了 trackedPath 或其某个后代。
func ApplyPayloadConfigWithRequestTracked(cfg *config.Config, model, protocol, fromProtocol, root string, payload, original []byte, requestedModel string, requestPath string, headers http.Header, trackedPath string) ([]byte, bool) {
	out, touched := ApplyPayloadConfigWithTrackedPaths(cfg, model, protocol, fromProtocol, root, payload, original, requestedModel, requestPath, headers, trackedPath)
	return out, touched[trackedPath]
}

func isImagesEndpointRequestPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if path == "/v1/images/generations" || path == "/v1/images/edits" {
		return true
	}
	// 对可能报告更长匹配路由的前缀路由器保持宽容。
	if strings.HasSuffix(path, "/v1/images/generations") || strings.HasSuffix(path, "/v1/images/edits") {
		return true
	}
	if strings.HasSuffix(path, "/images/generations") || strings.HasSuffix(path, "/images/edits") {
		return true
	}
	return false
}

// shouldStripImageGeneration 报告对于给定模式和请求路径，内置的 image_generation 工具
// 是否必须从出站载荷中移除。
//   - All：在所有端点上剥离。
//   - Chat：仅在非图片端点上剥离；在 /v1/images/* 端点上保留。
//   - Off / Passthrough：从不剥离。Off 会在别处注入该工具；Passthrough 原样转发
//     客户端载荷。
func shouldStripImageGeneration(mode config.DisableImageGenerationMode, requestPath string) bool {
	switch mode {
	case config.DisableImageGenerationAll:
		return true
	case config.DisableImageGenerationChat:
		return !isImagesEndpointRequestPath(requestPath)
	default:
		return false
	}
}

func payloadModelRulesMatch(rules []config.PayloadModelRule, protocol string, fromProtocol string, headers http.Header, payload []byte, root string, models []string) bool {
	if len(rules) == 0 || len(models) == 0 {
		return false
	}
	for _, model := range models {
		for _, entry := range rules {
			name := strings.TrimSpace(entry.Name)
			if name == "" {
				continue
			}
			if ep := strings.TrimSpace(entry.Protocol); ep != "" && protocol != "" && !strings.EqualFold(ep, protocol) {
				continue
			}
			if !payloadFromProtocolMatches(entry.FromProtocol, fromProtocol) {
				continue
			}
			if !payloadHeadersMatch(headers, entry.Headers) {
				continue
			}
			if !matchModelPattern(name, model) {
				continue
			}
			if payloadModelRuleConditionsMatch(payload, root, entry) {
				return true
			}
		}
	}
	return false
}

func payloadModelRuleConditionsMatch(payload []byte, root string, rule config.PayloadModelRule) bool {
	if !payloadMatchConditionsMatch(payload, root, rule.Match) {
		return false
	}
	if !payloadNotMatchConditionsMatch(payload, root, rule.NotMatch) {
		return false
	}
	if !payloadExistConditionsMatch(payload, root, rule.Exist) {
		return false
	}
	if !payloadNotExistConditionsMatch(payload, root, rule.NotExist) {
		return false
	}
	return true
}

func payloadMatchConditionsMatch(payload []byte, root string, conditions []map[string]any) bool {
	for _, condition := range conditions {
		for path, value := range condition {
			if strings.TrimSpace(path) == "" {
				continue
			}
			if !payloadPathMatchesValue(payload, buildPayloadPath(root, path), value) {
				return false
			}
		}
	}
	return true
}

func payloadNotMatchConditionsMatch(payload []byte, root string, conditions []map[string]any) bool {
	for _, condition := range conditions {
		for path, value := range condition {
			if strings.TrimSpace(path) == "" {
				continue
			}
			if payloadPathMatchesValue(payload, buildPayloadPath(root, path), value) {
				return false
			}
		}
	}
	return true
}

func payloadExistConditionsMatch(payload []byte, root string, paths []string) bool {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if !payloadPathExists(payload, buildPayloadPath(root, path)) {
			return false
		}
	}
	return true
}

func payloadNotExistConditionsMatch(payload []byte, root string, paths []string) bool {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if payloadPathExists(payload, buildPayloadPath(root, path)) {
			return false
		}
	}
	return true
}

func payloadPathMatchesValue(payload []byte, path string, value any) bool {
	for _, resolvedPath := range resolvePayloadRulePaths(payload, path) {
		result := gjson.GetBytes(payload, resolvedPath)
		if !result.Exists() {
			continue
		}
		if payloadResultEquals(result, value) {
			return true
		}
	}
	return false
}

func payloadPathExists(payload []byte, path string) bool {
	for _, resolvedPath := range resolvePayloadRulePaths(payload, path) {
		result := gjson.GetBytes(payload, resolvedPath)
		if result.Exists() && result.Type != gjson.Null {
			return true
		}
	}
	return false
}

func payloadResultEquals(result gjson.Result, value any) bool {
	actual, ok := normalizedPayloadResult(result)
	if !ok {
		return false
	}
	expected, ok := normalizedPayloadValue(value)
	if !ok {
		return false
	}
	return reflect.DeepEqual(actual, expected)
}

func normalizedPayloadResult(result gjson.Result) (any, bool) {
	if !result.Exists() {
		return nil, false
	}
	raw := strings.TrimSpace(result.Raw)
	if raw == "" {
		encoded, errMarshal := json.Marshal(result.Value())
		if errMarshal != nil {
			return nil, false
		}
		raw = string(encoded)
	}
	return normalizedPayloadJSON([]byte(raw))
}

func normalizedPayloadValue(value any) (any, bool) {
	encoded, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, false
	}
	return normalizedPayloadJSON(encoded)
}

func normalizedPayloadJSON(data []byte) (any, bool) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, false
	}
	var out any
	if errUnmarshal := json.Unmarshal(data, &out); errUnmarshal != nil {
		return nil, false
	}
	return out, true
}

func payloadFromProtocolMatches(pattern, fromProtocol string) bool {
	pattern = normalizePayloadFromProtocol(pattern)
	if pattern == "" {
		return true
	}
	fromProtocol = normalizePayloadFromProtocol(fromProtocol)
	if fromProtocol == "" {
		return false
	}
	return strings.EqualFold(pattern, fromProtocol)
}

func normalizePayloadFromProtocol(protocol string) string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	switch protocol {
	case "openai-response", "openai-responses", "response":
		return "responses"
	default:
		return protocol
	}
}

func payloadHeadersMatch(headers http.Header, rules map[string]string) bool {
	if len(rules) == 0 {
		return true
	}
	for key, pattern := range rules {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		values := payloadHeaderValues(headers, key)
		if len(values) == 0 {
			return false
		}
		matched := false
		for _, value := range values {
			if matchModelPattern(pattern, value) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func payloadHeaderValues(headers http.Header, key string) []string {
	if headers == nil {
		return nil
	}
	var values []string
	for headerKey, headerValues := range headers {
		if strings.EqualFold(headerKey, key) {
			values = append(values, headerValues...)
		}
	}
	return values
}

func payloadModelCandidates(model, requestedModel string) []string {
	model = strings.TrimSpace(model)
	requestedModel = strings.TrimSpace(requestedModel)
	if model == "" && requestedModel == "" {
		return nil
	}
	candidates := make([]string, 0, 3)
	seen := make(map[string]struct{}, 3)
	addCandidate := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, value)
	}
	if model != "" {
		addCandidate(model)
	}
	if requestedModel != "" {
		parsed := thinking.ParseSuffix(requestedModel)
		base := strings.TrimSpace(parsed.ModelName)
		if base != "" {
			addCandidate(base)
		}
		if parsed.HasSuffix {
			addCandidate(requestedModel)
		}
	}
	return candidates
}

// buildPayloadPath 把可选的 root 路径与相对参数路径组合起来。
// 当 root 为空时，参数路径按原样使用。当 root 非空时，
// 参数路径被视为相对于 root。
func buildPayloadPath(root, path string) string {
	r := strings.TrimSpace(root)
	p := strings.TrimSpace(path)
	if r == "" {
		return p
	}
	if p == "" {
		return r
	}
	if strings.HasPrefix(p, ".") {
		p = p[1:]
	}
	return r + "." + p
}

func payloadRuleTargetsPath(path, trackedPath string) bool {
	if trackedPath == "" || path == "" {
		return false
	}
	return path == trackedPath || strings.HasPrefix(path, trackedPath+".") || strings.HasPrefix(trackedPath, path+".")
}

func resolvePayloadRulePaths(payload []byte, path string) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if !strings.Contains(path, "#(") {
		return []string{path}
	}
	parts := splitPayloadRulePath(path)
	if len(parts) == 0 {
		return nil
	}
	paths := []string{""}
	for _, part := range parts {
		query, allMatches, ok := parsePayloadQueryPathPart(part)
		if !ok {
			for i := range paths {
				paths[i] = appendPayloadPathPart(paths[i], part)
			}
			continue
		}
		nextPaths := make([]string, 0, len(paths))
		for _, basePath := range paths {
			array := payloadValueAtPath(payload, basePath)
			if !array.Exists() || !array.IsArray() {
				continue
			}
			for index, item := range array.Array() {
				if !payloadQueryMatches(item, query) {
					continue
				}
				nextPaths = append(nextPaths, appendPayloadPathPart(basePath, strconv.Itoa(index)))
				if !allMatches {
					break
				}
			}
		}
		paths = nextPaths
		if len(paths) == 0 {
			return nil
		}
	}
	return paths
}

func splitPayloadRulePath(path string) []string {
	var parts []string
	start := 0
	depth := 0
	var quote byte
	escaped := false
	for i := 0; i < len(path); i++ {
		ch := path[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			continue
		}
		if ch == '(' {
			depth++
			continue
		}
		if ch == ')' {
			if depth > 0 {
				depth--
			}
			continue
		}
		if ch == '.' && depth == 0 {
			parts = append(parts, path[start:i])
			start = i + 1
		}
	}
	parts = append(parts, path[start:])
	return parts
}

func parsePayloadQueryPathPart(part string) (string, bool, bool) {
	if !strings.HasPrefix(part, "#(") {
		return "", false, false
	}
	closeIndex := findPayloadQueryClose(part)
	if closeIndex < 0 {
		return "", false, false
	}
	suffix := part[closeIndex+1:]
	if suffix != "" && suffix != "#" {
		return "", false, false
	}
	return strings.TrimSpace(part[2:closeIndex]), suffix == "#", true
}

func findPayloadQueryClose(part string) int {
	var quote byte
	escaped := false
	depth := 1
	for i := 2; i < len(part); i++ {
		ch := part[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			continue
		}
		if ch == '(' {
			depth++
			continue
		}
		if ch == ')' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func appendPayloadPathPart(path, part string) string {
	if path == "" {
		return part
	}
	if part == "" {
		return path
	}
	return path + "." + part
}

func payloadValueAtPath(payload []byte, path string) gjson.Result {
	if path == "" {
		return gjson.ParseBytes(payload)
	}
	return gjson.GetBytes(payload, path)
}

func payloadQueryMatches(item gjson.Result, query string) bool {
	for _, orPart := range splitPayloadLogical(query, "||") {
		if payloadQueryAndMatches(item, orPart) {
			return true
		}
	}
	return false
}

func payloadQueryAndMatches(item gjson.Result, query string) bool {
	parts := splitPayloadLogical(query, "&&")
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !payloadQueryTermMatches(item, part) {
			return false
		}
	}
	return true
}

func splitPayloadLogical(query, operator string) []string {
	var parts []string
	start := 0
	var quote byte
	escaped := false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			continue
		}
		if strings.HasPrefix(query[i:], operator) {
			parts = append(parts, strings.TrimSpace(query[start:i]))
			i += len(operator) - 1
			start = i + 1
		}
	}
	parts = append(parts, strings.TrimSpace(query[start:]))
	return parts
}

func payloadQueryTermMatches(item gjson.Result, term string) bool {
	term = strings.TrimSpace(term)
	if term == "" || item.Raw == "" {
		return false
	}
	wrapped := make([]byte, 0, len(item.Raw)+2)
	wrapped = append(wrapped, '[')
	wrapped = append(wrapped, item.Raw...)
	wrapped = append(wrapped, ']')
	return gjson.GetBytes(wrapped, "#("+term+")").Exists()
}

func removeToolTypeFromPayloadWithRoot(payload []byte, root string, toolType string) []byte {
	if len(payload) == 0 {
		return payload
	}
	toolType = strings.TrimSpace(toolType)
	if toolType == "" {
		return payload
	}
	toolsPath := buildPayloadPath(root, "tools")
	return removeToolTypeFromToolsArray(payload, toolsPath, toolType)
}

func removeToolChoiceFromPayloadWithRoot(payload []byte, root string, toolType string) []byte {
	if len(payload) == 0 {
		return payload
	}
	toolType = strings.TrimSpace(toolType)
	if toolType == "" {
		return payload
	}
	toolChoicePath := buildPayloadPath(root, "tool_choice")
	return removeToolChoiceFromPayload(payload, toolChoicePath, toolType)
}

func removeToolChoiceFromPayload(payload []byte, toolChoicePath string, toolType string) []byte {
	choice := gjson.GetBytes(payload, toolChoicePath)
	if !choice.Exists() {
		return payload
	}
	if choice.Type == gjson.String {
		if strings.EqualFold(strings.TrimSpace(choice.String()), toolType) {
			updated, errDel := sjson.DeleteBytes(payload, toolChoicePath)
			if errDel == nil {
				return updated
			}
		}
		return payload
	}
	if choice.Type != gjson.JSON {
		return payload
	}
	choiceType := strings.TrimSpace(choice.Get("type").String())
	if strings.EqualFold(choiceType, toolType) {
		updated, errDel := sjson.DeleteBytes(payload, toolChoicePath)
		if errDel == nil {
			return updated
		}
		return payload
	}
	if strings.EqualFold(choiceType, "tool") {
		name := strings.TrimSpace(choice.Get("name").String())
		if strings.EqualFold(name, toolType) {
			updated, errDel := sjson.DeleteBytes(payload, toolChoicePath)
			if errDel == nil {
				return updated
			}
		}
	}
	return payload
}

func removeToolTypeFromToolsArray(payload []byte, toolsPath string, toolType string) []byte {
	tools := gjson.GetBytes(payload, toolsPath)
	if !tools.Exists() || !tools.IsArray() {
		return payload
	}
	toolItems := tools.Array()
	removed := false
	for _, tool := range toolItems {
		if tool.Get("type").String() == toolType {
			removed = true
			break
		}
	}
	if !removed {
		return payload
	}
	filtered := make([][]byte, 0, len(toolItems))
	for _, tool := range toolItems {
		if tool.Get("type").String() != toolType {
			filtered = append(filtered, []byte(tool.Raw))
		}
	}
	updated, errSet := sjson.SetRawBytes(payload, toolsPath, JoinRawJSONArray(filtered))
	if errSet != nil {
		return payload
	}
	return updated
}

func setPayloadValueIfDifferent(payload []byte, path string, value any) []byte {
	updated, _ := setPayloadValueIfDifferentTracked(payload, path, value)
	return updated
}

func setPayloadValueIfDifferentTracked(payload []byte, path string, value any) ([]byte, bool) {
	current := gjson.GetBytes(payload, path)
	switch typed := value.(type) {
	case string:
		if current.Type == gjson.String && current.String() == typed {
			return payload, true
		}
	case bool:
		if (typed && current.Type == gjson.True) || (!typed && current.Type == gjson.False) {
			return payload, true
		}
	case nil:
		if current.Raw == "null" {
			return payload, true
		}
	default:
		expectedJSON, errSet := sjson.SetBytes([]byte(`{}`), "value", value)
		if errSet != nil {
			return payload, false
		}
		expected := gjson.GetBytes(expectedJSON, "value")
		if expected.Raw == "" {
			return payload, false
		}
		if len(current.Indexes) == 0 && current.Raw == expected.Raw {
			return payload, true
		}
		updated, errSet := sjson.SetRawBytes(payload, path, []byte(expected.Raw))
		if errSet != nil {
			return payload, false
		}
		return updated, true
	}
	updated, errSet := sjson.SetBytes(payload, path, value)
	if errSet != nil {
		return payload, false
	}
	return updated, true
}

func setPayloadRawValueIfDifferentTracked(payload []byte, path string, value []byte) ([]byte, bool) {
	current := gjson.GetBytes(payload, path)
	if current.Exists() && len(current.Indexes) == 0 && current.Raw == string(value) {
		return payload, true
	}
	updated, errSet := sjson.SetRawBytes(payload, path, value)
	if errSet != nil {
		return payload, false
	}
	return updated, true
}

func payloadRawValue(value any) ([]byte, bool) {
	if value == nil {
		return nil, false
	}
	switch typed := value.(type) {
	case string:
		return []byte(typed), true
	case []byte:
		return typed, true
	default:
		raw, errMarshal := json.Marshal(typed)
		if errMarshal != nil {
			return nil, false
		}
		return raw, true
	}
}

func PayloadRequestedModel(opts cliproxyexecutor.Options, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if len(opts.Metadata) == 0 {
		return fallback
	}
	raw, ok := opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey]
	if !ok || raw == nil {
		return fallback
	}
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return fallback
		}
		return strings.TrimSpace(v)
	case []byte:
		if len(v) == 0 {
			return fallback
		}
		trimmed := strings.TrimSpace(string(v))
		if trimmed == "" {
			return fallback
		}
		return trimmed
	default:
		return fallback
	}
}

func PayloadRequestPath(opts cliproxyexecutor.Options) string {
	if len(opts.Metadata) == 0 {
		return ""
	}
	raw, ok := opts.Metadata[cliproxyexecutor.RequestPathMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

// matchModelPattern 执行简单的通配符匹配，其中 '*' 匹配零个或多个字符。
// 示例：
//
//	"*-5" 匹配 "gpt-5"
//	"gpt-*" 匹配 "gpt-5" 和 "gpt-4"
//	"claude-*-sonnet" 匹配 "claude-3-7-sonnet"。
func matchModelPattern(pattern, model string) bool {
	pattern = strings.TrimSpace(pattern)
	model = strings.TrimSpace(model)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	// 迭代式 glob 风格匹配器，仅支持 '*' 通配符。
	pi, si := 0, 0
	starIdx := -1
	matchIdx := 0
	for si < len(model) {
		if pi < len(pattern) && (pattern[pi] == model[si]) {
			pi++
			si++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			starIdx = pi
			matchIdx = si
			pi++
			continue
		}
		if starIdx != -1 {
			pi = starIdx + 1
			matchIdx++
			si = matchIdx
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}
