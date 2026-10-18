package usageanalytics

import "strings"

// PlanDisplay fills blank LLM plan/quota fields from operator configuration.
// An empty model matches every model for the provider; a non-empty model is
// matched against the model, upstream model, or alias of the event.
type PlanDisplay struct {
	Provider  string
	Model     string
	Plan      string
	QuotaKind string
}

func normalizePlanDisplays(items []PlanDisplay) []PlanDisplay {
	if len(items) == 0 {
		return nil
	}
	normalized := make([]PlanDisplay, 0, len(items))
	for _, item := range items {
		item.Provider = strings.ToLower(strings.TrimSpace(item.Provider))
		item.Model = strings.TrimSpace(item.Model)
		item.Plan = strings.TrimSpace(item.Plan)
		item.QuotaKind = strings.ToLower(strings.TrimSpace(item.QuotaKind))
		if item.Provider == "" && item.Model == "" {
			continue
		}
		if item.Plan == "" && item.QuotaKind == "" {
			continue
		}
		normalized = append(normalized, item)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func applyPlanDisplay(event *Event, displays []PlanDisplay) {
	if event == nil || event.Kind != KindLLM || len(displays) == 0 {
		return
	}
	if event.Plan != "" && event.QuotaKind != "" {
		return
	}
	bestPlanScore := -1
	bestQuotaScore := -1
	var bestPlan string
	var bestQuota string
	for _, item := range displays {
		if item.Provider != "" && !strings.EqualFold(item.Provider, event.Provider) {
			continue
		}
		if item.Model != "" && !planDisplayMatchesModel(item.Model, event) {
			continue
		}
		score := 0
		if item.Provider != "" {
			score++
		}
		if item.Model != "" {
			score += 2
		}
		if item.Plan != "" && score > bestPlanScore {
			bestPlan = item.Plan
			bestPlanScore = score
		}
		if item.QuotaKind != "" && score > bestQuotaScore {
			bestQuota = item.QuotaKind
			bestQuotaScore = score
		}
	}
	if event.Plan == "" && bestPlanScore >= 0 {
		event.Plan = bestPlan
	}
	if event.QuotaKind == "" && bestQuotaScore >= 0 {
		event.QuotaKind = bestQuota
	}
}

func planDisplayMatchesModel(model string, event *Event) bool {
	for _, candidate := range []string{event.Model, event.UpstreamModel, event.Alias} {
		if candidate != "" && strings.EqualFold(model, candidate) {
			return true
		}
	}
	return false
}
