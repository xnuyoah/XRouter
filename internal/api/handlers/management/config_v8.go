package management

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/internal/clienterror"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	"gopkg.in/yaml.v3"
)

// ConfigV8ContextKey 标记由 v8 API 注册的、会保存配置的操作。
const ConfigV8ContextKey = "management.config-v8"

// ConfigV8 使用 v8 名称提供持久化的配置。GET 是只读视图；
// 只有成功的配置变更才会迁移已存储的文档。
//
// 读取会返回一个持有磁盘上配置字节 SHA-256 的 ETag。
// 写入会遵循针对该令牌的可选 If-Match，因此客户端可以
// 检测到在它读取之后有其他写入者保存了内容（412），而不是静默
// 覆盖该变更。没有 If-Match 时，写入保持无条件执行。
func (h *Handler) ConfigV8(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	raw, err := os.ReadFile(h.configFilePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read_failed"})
		return
	}
	diskHash := config.ComputeSourceHash(raw)
	if c.Request.Method != http.MethodGet && !ifMatchSatisfied(c.Request.Header.Values("If-Match"), diskHash) {
		c.Header("ETag", configV8ETag(diskHash))
		c.JSON(http.StatusPreconditionFailed, gin.H{"error": "precondition_failed"})
		return
	}
	data, _, err := config.NormalizeConfigLayout(raw, true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(data, &doc); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid_config"})
		return
	}
	root := doc.Content[0]
	path := strings.Trim(c.Param("path"), "/")
	parts := []string{}
	if path != "" {
		parts = strings.Split(path, "/")
	}
	yamlRequest := strings.HasSuffix(c.FullPath(), "/config.yaml")
	if c.Request.Method == http.MethodGet {
		config.ProjectV8ConfigAliases(root, strings.Join(parts, "."))
		if !yamlRequest {
			h.injectV8APIKeyAuthIndexesLocked(root, data)
		}
		value := configV8Node(root, parts)
		if value == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Header("ETag", configV8ETag(diskHash))
		if yamlRequest {
			c.Data(http.StatusOK, "application/yaml; charset=utf-8", data)
			return
		}
		var result any
		if err = value.Decode(&result); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "decode_failed"})
			return
		}
		c.JSON(http.StatusOK, result)
		return
	}
	before := cloneConfigV8Node(root)
	config.ProjectV8ConfigAliases(root, strings.Join(parts, "."))
	if c.Request.Method == http.MethodDelete {
		if len(parts) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot_delete_config"})
			return
		}
		if !deleteConfigV8Path(root, parts) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
	} else {
		body, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			c.JSON(clienterror.HTTPStatusFromErrorOr(errRead, http.StatusBadRequest), gin.H{"error": "invalid_body"})
			return
		}
		if !yamlRequest && !json.Valid(body) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
			return
		}
		var update yaml.Node
		if err = yaml.Unmarshal(body, &update); err != nil || len(update.Content) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
			return
		}
		stripAPIKeysAuthIndexesFromUpdate(parts, &update)
		if len(parts) == 0 && update.Content[0].Kind != yaml.MappingNode {
			c.JSON(http.StatusBadRequest, gin.H{"error": "config_must_be_object"})
			return
		}
		if len(parts) == 0 {
			if err = config.NormalizeV8ConfigAliases(update.Content[0]); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
				return
			}
		}
		// 路径标识 YAML key，绝不标识数组下标。列表是整体替换的。
		dst := root
		for _, part := range parts {
			if part == "" || dst.Kind != yaml.MappingNode {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_path"})
				return
			}
			next := configV8Node(dst, []string{part})
			if next == nil {
				next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				dst.Content = append(dst.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: part}, next)
			}
			dst = next
		}
		if c.Request.Method == http.MethodPatch {
			mergeConfigV8Patch(dst, update.Content[0])
		} else {
			if dst.Kind == yaml.ScalarNode && update.Content[0].Kind == yaml.ScalarNode {
				update.Content[0].HeadComment = dst.HeadComment
				update.Content[0].LineComment = dst.LineComment
				update.Content[0].FootComment = dst.FootComment
			}
			*dst = *update.Content[0]
		}
	}
	if err = config.NormalizeV8ConfigAliases(root); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	// Revisions 归 Home 所有，不得变成普通的可编辑设置。
	for _, field := range []string{"credentials/concurrency/lifecycle-config-revision", "credentials/concurrency/observation-barrier-revision", "plugins/auth-revision"} {
		old := configV8Node(before, strings.Split(field, "/"))
		next := configV8Node(root, strings.Split(field, "/"))
		var a, b any
		if old != nil {
			_ = old.Decode(&a)
		}
		if next != nil {
			_ = next.Decode(&b)
		}
		if !reflect.DeepEqual(a, b) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "read_only_field", "field": field})
			return
		}
	}
	stripAPIKeysAuthIndexesFromRoot(root)
	config.NormalizeCollectionNodeStyles(root)
	data, err = yaml.Marshal(&doc)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	next, err := config.ParseConfigBytes(data)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	if err = config.ValidateV8Config(data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	data, _, err = config.NormalizeConfigLayout(data, true)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	// 直接保存已校验的规范树：将运行时默认值投影
	// 回去会丢失显式的 null、空 map 以及不透明的插件设置。
	// WriteConfig 会保留已挂载配置文件的 inode。
	if err = WriteConfig(h.configFilePath, data); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write_failed", "message": err.Error()})
		return
	}
	// 仅运行时的归属状态绝不会从上传的 YAML 文档中获取。
	next.Home = h.cfg.Home
	// WriteConfig 可能会规范化字节，因此报告磁盘上实际的
	// 版本。如果无法读回，则省略 ETag，而不是猜测一个。
	// 与 Home 一样，SourceHash 保留其运行时归属者，并由重载
	// 发布，因此这里不会在下一次时被覆盖。
	if saved, errSaved := os.ReadFile(h.configFilePath); errSaved == nil {
		c.Header("ETag", configV8ETag(config.ComputeSourceHash(saved)))
	}
	h.cfg = next
	snapshot := h.reloadSnapshotConfigLocked()
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), snapshot)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "config-version": 8})
}

// configV8ETag 将配置源哈希渲染为强 ETag。
func configV8ETag(hash string) string {
	return `"` + hash + `"`
}

// ifMatchSatisfied 报告可选的 If-Match 头各行是否通过
// 针对 currentHash 的 RFC 9110 强比较。重复的行构成
// 一个列表。缺失该头总是通过；存在但为空的行
// 不携带任何 tag，判定为失败；整个字段为 "*" 时匹配任何当前
// 版本；弱实体标签和格式错误的实体标签永不匹配。
func ifMatchSatisfied(values []string, currentHash string) bool {
	if len(values) == 0 {
		return true
	}
	field := strings.TrimSpace(strings.Join(values, ","))
	if field == "*" {
		return true
	}
	for _, tag := range splitEntityTagList(field) {
		tag = strings.TrimSpace(tag)
		if len(tag) >= 2 && tag[0] == '"' && tag[len(tag)-1] == '"' && !strings.Contains(tag[1:len(tag)-1], `"`) && tag[1:len(tag)-1] == currentHash {
			return true
		}
	}
	return false
}

// splitEntityTagList 按列表逗号切分 If-Match 字段值。带引号的
// 实体标签内部的逗号是标签内容，而非分隔符。
func splitEntityTagList(field string) []string {
	parts := []string{}
	start := 0
	quoted := false
	for i := 0; i < len(field); i++ {
		switch field[i] {
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				parts = append(parts, field[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, field[start:])
}

func configV8Node(root *yaml.Node, parts []string) *yaml.Node {
	for _, part := range parts {
		if root == nil || root.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i < len(root.Content); i += 2 {
			if root.Content[i].Value == part {
				next = root.Content[i+1]
				break
			}
		}
		root = next
	}
	return root
}

// deleteConfigV8Path 只修剪被移除字段的空祖先。其他
// 显式的空 map 可能承载继承或插件语义，保持不动。
func deleteConfigV8Path(root *yaml.Node, parts []string) bool {
	if root == nil || root.Kind != yaml.MappingNode || len(parts) == 0 {
		return false
	}
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value != parts[0] {
			continue
		}
		if len(parts) > 1 {
			child := root.Content[i+1]
			if !deleteConfigV8Path(child, parts[1:]) {
				return false
			}
			if len(child.Content) > 0 {
				return true
			}
		}
		root.Content = append(root.Content[:i], root.Content[i+2:]...)
		return true
	}
	return false
}

func cloneConfigV8Node(node *yaml.Node) *yaml.Node {
	copy := *node
	copy.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		copy.Content[i] = cloneConfigV8Node(child)
	}
	return &copy
}

// 与 JSON merge-patch 不同，null 会被保留：可选的 key 覆盖用它来
// 继承组的值。DELETE 是显式的字段移除操作。
func mergeConfigV8Patch(dst, src *yaml.Node) {
	if dst.Kind != yaml.MappingNode || src.Kind != yaml.MappingNode {
		if dst.Kind == yaml.ScalarNode && src.Kind == yaml.ScalarNode {
			copy := *src
			copy.HeadComment, copy.LineComment, copy.FootComment = dst.HeadComment, dst.LineComment, dst.FootComment
			*dst = copy
			return
		}
		*dst = *src
		return
	}
	for i := 0; i < len(src.Content); i += 2 {
		key, value := src.Content[i], src.Content[i+1]
		if old := configV8Node(dst, []string{key.Value}); old != nil {
			mergeConfigV8Patch(old, value)
		} else {
			dst.Content = append(dst.Content, key, value)
		}
	}
}
