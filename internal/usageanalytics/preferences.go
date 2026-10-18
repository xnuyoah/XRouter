package usageanalytics

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.etcd.io/bbolt"
)

var dashboardPreferencesKey = []byte("dashboard_preferences")

var allowedLogColumns = map[string]struct{}{
	"time": {}, "kind": {}, "provider": {}, "model": {}, "account": {},
	"plan": {}, "tokens": {}, "cost": {}, "latency": {}, "request": {},
	"request_id": {},
}

// Preferences stores per-installation usage page defaults.
type Preferences struct {
	Kind             string `json:"kind"`
	Range            string `json:"range"`
	GroupBy          string `json:"group_by"`
	TokenDisplayMode string `json:"token_display_mode"`
	PageSize         int    `json:"page_size"`
	// LowBalanceThresholdPercent marks search-account balances at or below this
	// percentage of their limit as low. Allowed values: 5, 10, 20, 30.
	LowBalanceThresholdPercent int `json:"low_balance_threshold_percent"`
	// LogColumns narrows the request-log table to the listed columns. Empty
	// means all supported columns.
	LogColumns []string  `json:"log_columns,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// DefaultPreferences returns the built-in usage page defaults.
func DefaultPreferences() Preferences {
	return Preferences{
		Kind:                       "all",
		Range:                      "today",
		GroupBy:                    "provider",
		TokenDisplayMode:           "compact",
		PageSize:                   50,
		LowBalanceThresholdPercent: 10,
	}
}

func normalizePreferences(input Preferences) (Preferences, error) {
	out := input
	switch strings.ToLower(strings.TrimSpace(out.Kind)) {
	case "", "all":
		out.Kind = "all"
	case "llm":
		out.Kind = "llm"
	case "search":
		out.Kind = "search"
	default:
		return Preferences{}, fmt.Errorf("kind must be all, llm or search")
	}
	switch strings.ToLower(strings.TrimSpace(out.Range)) {
	case "", "today":
		out.Range = "today"
	case "24h", "5h", "7d", "30d", "month", "custom":
		out.Range = strings.ToLower(strings.TrimSpace(out.Range))
	default:
		return Preferences{}, fmt.Errorf("unsupported range %q", out.Range)
	}
	out.GroupBy = strings.ToLower(strings.TrimSpace(out.GroupBy))
	switch out.GroupBy {
	case "", "provider":
		out.GroupBy = "provider"
	case "kind", "model", "channel", "operation", "plan", "quota_kind", "account", "source",
		"auth_type", "executor", "service_tier", "reasoning_effort", "status":
	default:
		return Preferences{}, fmt.Errorf("unsupported group_by %q", out.GroupBy)
	}
	switch strings.ToLower(strings.TrimSpace(out.TokenDisplayMode)) {
	case "", "compact":
		out.TokenDisplayMode = "compact"
	case "full", "k", "m", "b":
		out.TokenDisplayMode = strings.ToLower(strings.TrimSpace(out.TokenDisplayMode))
	default:
		return Preferences{}, fmt.Errorf("token_display_mode must be compact, full, k, m or b")
	}
	switch out.PageSize {
	case 0:
		out.PageSize = 50
	case 25, 50, 100, 200:
	default:
		return Preferences{}, fmt.Errorf("page_size must be 25, 50, 100 or 200")
	}
	switch out.LowBalanceThresholdPercent {
	case 0:
		out.LowBalanceThresholdPercent = 10
	case 5, 10, 20, 30:
	default:
		return Preferences{}, fmt.Errorf("low_balance_threshold_percent must be 5, 10, 20 or 30")
	}
	if len(out.LogColumns) > 0 {
		seen := make(map[string]struct{}, len(out.LogColumns))
		columns := make([]string, 0, len(out.LogColumns))
		for _, column := range out.LogColumns {
			column = strings.ToLower(strings.TrimSpace(column))
			if _, ok := allowedLogColumns[column]; !ok {
				return Preferences{}, fmt.Errorf("unsupported log column %q", column)
			}
			if _, exists := seen[column]; exists {
				continue
			}
			seen[column] = struct{}{}
			columns = append(columns, column)
		}
		if len(columns) == 0 {
			columns = nil
		}
		out.LogColumns = columns
	}
	out.UpdatedAt = time.Time{}
	return out, nil
}

// Preferences returns the current usage page defaults.
func (s *Store) Preferences() Preferences {
	if s == nil {
		return DefaultPreferences()
	}
	s.prefsMu.RLock()
	defer s.prefsMu.RUnlock()
	return s.preferences
}

// SavePreferences validates and persists usage page defaults.
func (s *Store) SavePreferences(input Preferences) (Preferences, error) {
	if s == nil {
		return Preferences{}, fmt.Errorf("usage analytics store is nil")
	}
	normalized, errNormalize := normalizePreferences(input)
	if errNormalize != nil {
		return Preferences{}, errNormalize
	}
	normalized.UpdatedAt = time.Now().UTC()
	encoded, errMarshal := json.Marshal(normalized)
	if errMarshal != nil {
		return Preferences{}, errMarshal
	}
	if errPut := s.db.Update(func(tx *bbolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		if meta == nil {
			return fmt.Errorf("usage analytics meta bucket is missing")
		}
		return meta.Put(dashboardPreferencesKey, encoded)
	}); errPut != nil {
		return Preferences{}, errPut
	}
	s.prefsMu.Lock()
	s.preferences = normalized
	s.prefsMu.Unlock()
	return normalized, nil
}

func (s *Store) loadPreferences() {
	preferences := DefaultPreferences()
	_ = s.db.View(func(tx *bbolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		if meta == nil {
			return nil
		}
		raw := meta.Get(dashboardPreferencesKey)
		if len(raw) == 0 {
			return nil
		}
		var stored Preferences
		if errUnmarshal := json.Unmarshal(raw, &stored); errUnmarshal != nil {
			return nil
		}
		if normalized, errNormalize := normalizePreferences(stored); errNormalize == nil {
			preferences = normalized
			if stored.UpdatedAt.IsZero() {
				preferences.UpdatedAt = time.Time{}
			} else {
				preferences.UpdatedAt = stored.UpdatedAt
			}
		}
		return nil
	})
	s.prefsMu.Lock()
	s.preferences = preferences
	s.prefsMu.Unlock()
}
