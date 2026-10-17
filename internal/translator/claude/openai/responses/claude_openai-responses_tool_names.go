package responses

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/xnuyoah/XRouter/v8/internal/util"
)

var claudeToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// claudeToolNames 将带限定符的 Responses 工具标识映射为唯一的 Claude
// 工具名（针对单个请求），并支持反向映射。请求和响应转换器
// 从相同的 Responses JSON 构建它，因此两侧保持一致。
type claudeToolNames struct {
	toClaude   map[string]string
	fromClaude map[string]string
}

func buildClaudeToolNames(root gjson.Result, toolSearch bool) claudeToolNames {
	return buildClaudeToolNamesWithWinners(root, responsesToolWinners(root, toolSearch), toolSearch)
}

func buildClaudeToolNamesWithWinners(root gjson.Result, winners map[string]responsesToolDescriptor, toolSearch bool) claudeToolNames {
	m := claudeToolNames{toClaude: map[string]string{}, fromClaude: map[string]string{}}
	taken := map[string]bool{}
	var declared []string
	for _, d := range responsesToolDescriptors(root, toolSearch) {
		if w, ok := winners[d.name]; !ok || w.order != d.order {
			continue
		}
		switch d.toolType {
		case "function", "custom":
			declared = append(declared, d.name)
		default:
			m.assign(d.name, d.name, taken)
		}
	}
	m.allocate(declared, taken)
	m.allocate(responsesHistoryToolIdentities(root), taken)
	return m
}

// claudeName 返回带限定符的 Responses 标识对应的 Claude 名称。
func (m claudeToolNames) claudeName(identity string) string {
	if name, ok := m.toClaude[identity]; ok {
		return name
	}
	return util.SanitizeClaudeFunctionName(identity)
}

// identity 返回 Claude 名称对应的带限定符的 Responses 标识。
// 未知名称原样返回。
func (m claudeToolNames) identity(claudeName string) string {
	if identity, ok := m.fromClaude[claudeName]; ok {
		return identity
	}
	return claudeName
}

func (m claudeToolNames) assign(identity, name string, taken map[string]bool) {
	m.toClaude[identity] = name
	m.fromClaude[name] = identity
	taken[name] = true
}

func (m claudeToolNames) allocate(identities []string, taken map[string]bool) {
	var changed []string
	seen := map[string]bool{}
	for _, id := range identities {
		if _, done := m.toClaude[id]; done || id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if claudeToolNamePattern.MatchString(id) && !taken[id] {
			m.assign(id, id, taken)
			continue
		}
		changed = append(changed, id)
	}
	count := map[string]int{}
	for _, id := range changed {
		count[util.SanitizeClaudeFunctionName(id)]++
	}
	var hashed []string
	for _, id := range changed {
		if base := util.SanitizeClaudeFunctionName(id); count[base] == 1 && !taken[base] {
			m.assign(id, base, taken)
		} else {
			hashed = append(hashed, id)
		}
	}
	sort.Strings(hashed)
	for _, id := range hashed {
		base := util.SanitizeClaudeFunctionName(id)
		if len(base) > 53 {
			base = base[:53]
		}
		for n := 0; ; n++ {
			seed := id
			if n > 0 {
				seed += "\x00" + strconv.Itoa(n)
			}
			sum := sha256.Sum256([]byte(seed))
			if name := base + "_" + hex.EncodeToString(sum[:])[:10]; !taken[name] {
				m.assign(id, name, taken)
				break
			}
		}
	}
}

// responsesHistoryToolIdentities 按顺序列出 input 中 function_call 和
// custom_tool_call 项的带限定符名称。
func responsesHistoryToolIdentities(root gjson.Result) []string {
	var ids []string
	input := root.Get("input")
	if input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			switch item.Get("type").String() {
			case "function_call", "custom_tool_call":
				name := item.Get("name").String()
				if ns := strings.TrimSpace(item.Get("namespace").String()); ns != "" {
					name = qualifyResponsesNamespaceToolName(ns, name)
				}
				if name != "" {
					ids = append(ids, name)
				}
			}
			return true
		})
	}
	return ids
}
