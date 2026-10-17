package util

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// inPlaceSJSONTokens 是那些让写操作复用调用方底层数组
// 而不是分配新数组的 sjson 开关。
var inPlaceSJSONTokens = []string{"ReplaceInPlace", "Optimistic"}

// inPlaceSJSONAllowlist 保存允许选择启用原地 sjson 写操作的
// 文件。只有当证明在相应时点，从同一缓冲区派生出的
// 无拷贝 GJSON 结果（GetGJSONBytesNoCopy / ParseGJSONBytesNoCopy）
// 不可能仍然存活时，才能将文件加入此列表。
var inPlaceSJSONAllowlist = map[string]struct{}{}

// forEachSourceFile 遍历仓库中每个非测试的 Go 文件。
func forEachSourceFile(t *testing.T, root string, visit func(rel string, data []byte)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			switch d.Name() {
			case "vendor", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, errRel := filepath.Rel(root, path)
		if errRel != nil {
			return errRel
		}
		data, errRead := os.ReadFile(path)
		if errRead != nil {
			return errRead
		}
		visit(filepath.ToSlash(rel), data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}
}

// TestNoInPlaceSJSONWrites 保护这一不变量：请求载荷缓冲区
// 在其整个生命周期内保持不可变。
//
// GetGJSONBytesNoCopy 和 ParseGJSONBytesNoCopy 会交出 Raw 与 Str
// 别名到调用方 []byte 的 gjson.Result 值。Go 字符串绝不能变化，
// 因此对该缓冲区的任何原地修改都会让已经派生出的结果变成
// 静默错误的数据：重新解析会看到新的字节，而那些
// 曾用作 map key 的字符串保留着按旧字节计算出的哈希。竞态检测器
// 看不到这一点，普通测试也很少触发它，因此这里改为
// 以静态方式强制该不变量。
func TestNoInPlaceSJSONWrites(t *testing.T) {
	root := repoRoot(t)
	var offenders []string
	forEachSourceFile(t, root, func(rel string, data []byte) {
		if _, allowed := inPlaceSJSONAllowlist[rel]; allowed {
			return
		}
		for _, token := range inPlaceSJSONTokens {
			if strings.Contains(string(data), token) {
				offenders = append(offenders, rel+" uses "+token)
			}
		}
	})
	if len(offenders) > 0 {
		t.Fatalf("in-place sjson writes would corrupt no-copy GJSON results that alias the same buffer:\n  %s\n"+
			"Either keep the default (allocating) sjson call, or prove no no-copy result derived from that buffer is still alive and add the file to inPlaceSJSONAllowlist.",
			strings.Join(offenders, "\n  "))
	}
}

// inPlaceByteWritePatterns 匹配 Go 代码覆盖已有缓冲区字节的
// 现实方式：向切片表达式复制，或在循环中
// 将元素清零。它们无法捕获所有可能的形式，因此只是针对
// 新代码的报警线，而非不存在此类写法的证明。
var inPlaceByteWritePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bcopy\([a-zA-Z_][A-Za-z0-9_.]*\[`),
	regexp.MustCompile(`^\s*[a-zA-Z_][A-Za-z0-9_.]*\[[a-zA-Z0-9_]+\] = 0\r?$`),
}

// reviewedInPlaceByteWrites 记录每个文件中已审查过的原地字节写操作。
// 计数是约定的一部分：在已审查过的文件中新增写操作
// 同样必须被审查，因此计数必须有意识地更新。每条理由
// 说明该写操作为何不会破坏无拷贝 GJSON 结果，或者因为该
// 缓冲区为写者私有，或者因为每个读取者都会先复制出来。
type reviewedInPlaceByteWrite struct {
	count  int
	reason string
}

var reviewedInPlaceByteWrites = map[string]reviewedInPlaceByteWrite{
	"internal/translator/common/apply_patch_responses.go": {1, "shifts [][]byte item references in a private output slice; no byte of any JSON payload is rewritten"},
	"internal/home/client.go":                             {1, "zeroes a secret buffer after json.Unmarshal has copied every value out"},
	"internal/pluginstore/auth.go":                        {1, "zeroes a locally built credential buffer after base64 encoding copied it out"},
}

// TestInPlaceByteWritesAreReviewed 使原地字节写操作集合保持小巧
// 且有据可依。对该集合的任何改动，包括在已审查过的文件中
// 新增写操作，都会失败，直到作者证明从该缓冲区派生出的
// 无拷贝 GJSON 结果不可能仍然存活并将其记录在上方。
func TestInPlaceByteWritesAreReviewed(t *testing.T) {
	root := repoRoot(t)
	found := make(map[string][]string)
	forEachSourceFile(t, root, func(rel string, data []byte) {
		normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
		for _, line := range strings.Split(normalized, "\n") {
			for _, pattern := range inPlaceByteWritePatterns {
				if pattern.MatchString(line) {
					found[rel] = append(found[rel], strings.TrimSpace(line))
				}
			}
		}
	})
	for rel, lines := range found {
		reviewed, ok := reviewedInPlaceByteWrites[rel]
		if !ok {
			t.Errorf("unreviewed in-place byte write in %s:\n  %s\nProve that no no-copy GJSON result derived from that buffer is still alive, then record it in reviewedInPlaceByteWrites.",
				rel, strings.Join(lines, "\n  "))
			continue
		}
		if len(lines) != reviewed.count {
			t.Errorf("%s has %d in-place byte write(s), reviewed %d (%s):\n  %s",
				rel, len(lines), reviewed.count, reviewed.reason, strings.Join(lines, "\n  "))
		}
	}
	for rel := range reviewedInPlaceByteWrites {
		if _, ok := found[rel]; !ok {
			t.Errorf("stale entry in reviewedInPlaceByteWrites: %s no longer contains an in-place byte write", rel)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, errStat := os.Stat(filepath.Join(dir, "go.mod")); errStat == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above working directory")
		}
		dir = parent
	}
}

func TestInPlaceByteWritePatterns_CRLF(t *testing.T) {
	crlfLine := "\traw[index] = 0\r"
	matched := false
	for _, pattern := range inPlaceByteWritePatterns {
		if pattern.MatchString(crlfLine) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("inPlaceByteWritePatterns failed to match CRLF line %q", crlfLine)
	}
}

func TestForEachSourceFile_SkipsDotDirs(t *testing.T) {
	tempDir := t.TempDir()
	dotDir := filepath.Join(tempDir, ".gomodcache")
	if err := os.MkdirAll(dotDir, 0755); err != nil {
		t.Fatal(err)
	}
	sampleFile := filepath.Join(dotDir, "sample.go")
	if err := os.WriteFile(sampleFile, []byte("package sample\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var visited []string
	forEachSourceFile(t, tempDir, func(rel string, data []byte) {
		visited = append(visited, rel)
	})
	if len(visited) > 0 {
		t.Fatalf("forEachSourceFile should have skipped dot directories, but visited: %v", visited)
	}
}
