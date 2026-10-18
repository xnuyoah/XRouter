package websearch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Quota history retention and compaction limits.
const (
	quotaHistoryDays        = 30
	quotaHistoryMinInterval = time.Minute
	maxQuotaHistoryBytes    = 4 << 20
	quotaHistoryDirName     = "quota-history"
)

// QuotaHistoryPoint is one balance observation for a key.
type QuotaHistoryPoint struct {
	CheckedAt time.Time          `json:"checked_at"`
	Status    QuotaRefreshStatus `json:"status"`
	Balances  []QuotaBalance     `json:"balances,omitempty"`
	ResetAt   *time.Time         `json:"reset_at,omitempty"`
	Message   string             `json:"message,omitempty"`
}

// AppendQuotaHistory records one balance observation. Duplicate observations
// within a minute are skipped to keep manual refreshes and post-call refreshes
// from producing noise.
func (s *Store) AppendQuotaHistory(channel Channel, accountID string, snapshot *QuotaSnapshot, status QuotaRefreshStatus, message string, now time.Time) error {
	if s == nil {
		return fmt.Errorf("状态存储未初始化")
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	point := QuotaHistoryPoint{
		CheckedAt: now,
		Status:    status,
		Message:   message,
	}
	if snapshot != nil {
		point.Balances = append([]QuotaBalance(nil), snapshot.Balances...)
		point.ResetAt = snapshot.ResetAt
		if len(point.Balances) == 0 && point.Message == "" {
			point.Message = snapshot.Message
		}
	}

	path := s.quotaHistoryPath(channel, accountID)
	s.mu.Lock()
	defer s.mu.Unlock()

	if last, ok := readLastQuotaHistoryPoint(path); ok {
		if now.Sub(last.CheckedAt) < quotaHistoryMinInterval &&
			last.Status == point.Status &&
			sameBalances(last.Balances, point.Balances) &&
			sameTimePointer(last.ResetAt, point.ResetAt) &&
			last.Message == point.Message {
			return nil
		}
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return fmt.Errorf("create quota history directory: %w", errMkdir)
	}
	encoded, errMarshal := json.Marshal(point)
	if errMarshal != nil {
		return fmt.Errorf("encode quota history point: %w", errMarshal)
	}
	file, errOpen := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if errOpen != nil {
		return fmt.Errorf("open quota history: %w", errOpen)
	}
	if _, errWrite := file.Write(append(encoded, '\n')); errWrite != nil {
		_ = file.Close()
		return fmt.Errorf("write quota history: %w", errWrite)
	}
	if errClose := file.Close(); errClose != nil {
		return fmt.Errorf("close quota history: %w", errClose)
	}
	if info, errStat := os.Stat(path); errStat == nil && info.Size() > maxQuotaHistoryBytes {
		s.compactQuotaHistoryLocked(path, now)
	}
	return nil
}

// QuotaHistory returns points for one key in ascending time order.
func (s *Store) QuotaHistory(channel Channel, accountID string, hours, limit int, now time.Time) []QuotaHistoryPoint {
	if s == nil {
		return nil
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if hours <= 0 || hours > quotaHistoryDays*24 {
		hours = quotaHistoryDays * 24
	}
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	now = now.UTC()
	cutoff := now.Add(-time.Duration(hours) * time.Hour)
	path := s.quotaHistoryPath(channel, accountID)
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	points := make([]QuotaHistoryPoint, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var point QuotaHistoryPoint
		if errUnmarshal := json.Unmarshal([]byte(trimmed), &point); errUnmarshal != nil {
			continue
		}
		if point.CheckedAt.Before(cutoff) || point.CheckedAt.After(now) {
			continue
		}
		points = append(points, point)
	}
	sort.Slice(points, func(i, j int) bool {
		return points[i].CheckedAt.Before(points[j].CheckedAt)
	})
	if len(points) > limit {
		points = points[len(points)-limit:]
	}
	return points
}

func (s *Store) quotaHistoryPath(channel Channel, accountID string) string {
	return filepath.Join(s.dir, quotaHistoryDirName, safeFileComponent(string(channel))+"-"+safeFileComponent(accountID)+".jsonl")
}

func (s *Store) compactQuotaHistoryLocked(path string, now time.Time) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return
	}
	cutoff := now.Add(-quotaHistoryDays * 24 * time.Hour)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var point QuotaHistoryPoint
		if errUnmarshal := json.Unmarshal([]byte(trimmed), &point); errUnmarshal != nil {
			continue
		}
		if point.CheckedAt.Before(cutoff) {
			continue
		}
		kept = append(kept, trimmed)
	}
	if len(kept) == len(lines) {
		return
	}
	body := ""
	if len(kept) > 0 {
		body = strings.Join(kept, "\n") + "\n"
	}
	tempPath := path + ".tmp"
	if errWrite := os.WriteFile(tempPath, []byte(body), 0o600); errWrite != nil {
		logWarnf("压缩额度历史失败: %v", errWrite)
		return
	}
	if errRename := os.Rename(tempPath, path); errRename != nil {
		logWarnf("替换额度历史失败: %v", errRename)
	}
}

func readLastQuotaHistoryPoint(path string) (QuotaHistoryPoint, bool) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return QuotaHistoryPoint{}, false
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		trimmed := strings.TrimSpace(lines[index])
		if trimmed == "" {
			continue
		}
		var point QuotaHistoryPoint
		if errUnmarshal := json.Unmarshal([]byte(trimmed), &point); errUnmarshal != nil {
			continue
		}
		return point, true
	}
	return QuotaHistoryPoint{}, false
}

func sameBalances(left, right []QuotaBalance) bool {
	return reflect.DeepEqual(left, right)
}

func sameTimePointer(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func safeFileComponent(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "unknown"
	}
	var builder strings.Builder
	for _, char := range trimmed {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '-', char == '_':
			builder.WriteRune(char)
		default:
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "unknown"
	}
	return builder.String()
}
