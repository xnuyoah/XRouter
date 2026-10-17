package auth

import (
	"fmt"
	"strconv"

	"github.com/xnuyoah/XRouter/v8/internal/credentialweight"
)

// ValidateAuthWeight 校验每个显式的凭据权重来源。
func ValidateAuthWeight(auth *Auth) error {
	if auth == nil {
		return nil
	}
	if rawWeight, ok := auth.Attributes[AttributeWeight]; ok {
		if _, errParse := credentialweight.ParseString(rawWeight); errParse != nil {
			return fmt.Errorf("invalid attributes weight: %w", errParse)
		}
	}
	if rawWeight, ok := auth.Metadata[AttributeWeight]; ok {
		if _, errParse := credentialweight.ParseValue(rawWeight); errParse != nil {
			return fmt.Errorf("invalid metadata weight: %w", errParse)
		}
	}
	return nil
}

// ApplyAuthWeightMetadata 校验 auth 并应用来源元数据权重。
func ApplyAuthWeightMetadata(auth *Auth, metadata map[string]any) error {
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return errWeight
	}
	if auth == nil || metadata == nil {
		return nil
	}
	rawWeight, ok := metadata[AttributeWeight]
	if !ok {
		return nil
	}
	weight, errParse := credentialweight.ParseValue(rawWeight)
	if errParse != nil {
		return fmt.Errorf("invalid metadata weight: %w", errParse)
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[AttributeWeight] = strconv.FormatInt(weight, 10)
	return nil
}
