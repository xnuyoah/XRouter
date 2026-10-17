package common

import (
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

// AlignOpenAIToolCallMessages 重排工具结果消息，使其按 tool_call_id
// 紧跟在发出匹配 tool_calls 的 assistant 消息之后。
// 它保留原始消息顺序、内容部分、推理字段以及数值
// 精度，同时不动有歧义、孤儿和不完整的历史记录。
func AlignOpenAIToolCallMessages(messages [][]byte, extraAmbiguousIDs ...string) [][]byte {
	if len(messages) <= 1 {
		return messages
	}

	type assistantRecord struct {
		msgIndex            int
		callIDs             []string
		hasInvalidOrEmptyID bool
	}

	assistants := make([]assistantRecord, 0)
	assistantByCallID := make(map[string]int)
	ambiguousCallIDs := make(map[string]bool)
	for _, id := range extraAmbiguousIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			ambiguousCallIDs[trimmed] = true
		}
	}
	toolMsgIndicesByCallID := make(map[string][]int)

	for i, raw := range messages {
		role := gjson.GetBytes(raw, "role").String()
		switch role {
		case "assistant":
			toolCalls := gjson.GetBytes(raw, "tool_calls")
			if toolCalls.Exists() && toolCalls.IsArray() {
				rawCalls := toolCalls.Array()
				if len(rawCalls) > 0 {
					callIDs := make([]string, 0, len(rawCalls))
					hasEmptyCallID := false
					for _, tc := range rawCalls {
						callID := tc.Get("id").String()
						if callID == "" {
							// 空的 tool_call_id 无法安全匹配。
							ambiguousCallIDs[""] = true
							hasEmptyCallID = true
							continue
						}
						if _, exists := assistantByCallID[callID]; exists {
							ambiguousCallIDs[callID] = true
						}
						assistantByCallID[callID] = i
						callIDs = append(callIDs, callID)
					}
					if len(callIDs) > 0 || hasEmptyCallID {
						assistants = append(assistants, assistantRecord{
							msgIndex:            i,
							callIDs:             callIDs,
							hasInvalidOrEmptyID: hasEmptyCallID,
						})
					}
				}
			}

		case "tool":
			callID := gjson.GetBytes(raw, "tool_call_id").String()
			if callID == "" {
				ambiguousCallIDs[""] = true
			} else {
				toolMsgIndicesByCallID[callID] = append(toolMsgIndicesByCallID[callID], i)
				if len(toolMsgIndicesByCallID[callID]) > 1 {
					ambiguousCallIDs[callID] = true
				}
			}
		}
	}

	if len(assistants) == 0 {
		return messages
	}

	type reorderGroup struct {
		assistantIndex int
		toolIndices    []int
	}

	groups := make([]reorderGroup, 0)
	needsReorder := false

	for _, ast := range assistants {
		if ast.hasInvalidOrEmptyID {
			continue
		}
		// 校验该 assistant 的完整性与歧义性。
		isEligible := true
		matchedToolIndices := make([]int, 0, len(ast.callIDs))

		for _, callID := range ast.callIDs {
			if ambiguousCallIDs[callID] {
				isEligible = false
				break
			}
			indices := toolMsgIndicesByCallID[callID]
			if len(indices) != 1 {
				// 不完整或孤儿：必须恰好有一个工具消息匹配。
				isEligible = false
				break
			}
			toolIdx := indices[0]
			if toolIdx <= ast.msgIndex {
				// 因果顺序违规：工具结果出现在 assistant 之前。
				isEligible = false
				break
			}
			matchedToolIndices = append(matchedToolIndices, toolIdx)
		}

		if !isEligible {
			continue
		}

		// 对工具消息下标排序，以保留它们的相对顺序。
		sort.Ints(matchedToolIndices)

		// 检查工具消息是否已经紧跟在 assistant 之后。
		alreadyAdjacent := true
		for offset, toolIdx := range matchedToolIndices {
			if toolIdx != ast.msgIndex+offset+1 {
				alreadyAdjacent = false
				break
			}
		}

		if !alreadyAdjacent {
			needsReorder = true
			groups = append(groups, reorderGroup{
				assistantIndex: ast.msgIndex,
				toolIndices:    matchedToolIndices,
			})
		}
	}

	if !needsReorder {
		return messages
	}

	movedToolIndices := make(map[int]bool)
	toolsToInsert := make(map[int][][]byte)

	for _, g := range groups {
		toolList := make([][]byte, 0, len(g.toolIndices))
		for _, idx := range g.toolIndices {
			movedToolIndices[idx] = true
			toolList = append(toolList, messages[idx])
		}
		toolsToInsert[g.assistantIndex] = toolList
	}

	reordered := make([][]byte, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		if movedToolIndices[i] {
			continue
		}
		reordered = append(reordered, messages[i])
		if tools, ok := toolsToInsert[i]; ok {
			reordered = append(reordered, tools...)
		}
	}

	return reordered
}
