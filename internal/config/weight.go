package config

import (
	"fmt"

	"github.com/xnuyoah/XRouter/v8/internal/credentialweight"
	"gopkg.in/yaml.v3"
)

// MaxCredentialWeight 是最大的正凭据路由权重。
const MaxCredentialWeight = int(credentialweight.Max)

// ValidateCredentialWeight 校验单个可选的配置凭据权重。
func ValidateCredentialWeight(weight *int) error {
	if weight == nil {
		return nil
	}
	_, errNormalize := credentialweight.Normalize(int64(*weight))
	return errNormalize
}

func validateCredentialWeightYAML(data []byte) error {
	var document yaml.Node
	if errUnmarshal := yaml.Unmarshal(data, &document); errUnmarshal != nil {
		return nil
	}
	if len(document.Content) == 0 {
		return nil
	}
	root, err := flattenV8(document.Content[0])
	if err != nil {
		return err
	}
	families := map[string]struct{}{
		"openai-api-key": {}, "deepseek-api-key": {}, "nvidia-api-key": {}, "agnes-api-key": {},
	}
	for index := 0; root != nil && root.Kind == yaml.MappingNode && index+1 < len(root.Content); index += 2 {
		name := root.Content[index].Value
		value := root.Content[index+1]
		if _, ok := families[name]; ok {
			if errValidate := validateWeightSequenceNode(value, name); errValidate != nil {
				return errValidate
			}
			continue
		}
		if name == "openai-compatibility" {
			if errValidate := validateOpenAICompatibilityWeightNodes(value); errValidate != nil {
				return errValidate
			}
		}
	}
	return nil
}

func validateWeightSequenceNode(sequence *yaml.Node, path string) error {
	if sequence == nil || sequence.Kind != yaml.SequenceNode {
		return nil
	}
	for index, item := range sequence.Content {
		if errValidate := validateWeightMappingNode(item, fmt.Sprintf("%s[%d]", path, index)); errValidate != nil {
			return errValidate
		}
	}
	return nil
}

func validateWeightMappingNode(mapping *yaml.Node, path string) error {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value != "weight" {
			continue
		}
		value := mapping.Content[index+1]
		if value.Kind != yaml.ScalarNode || value.Tag != "!!int" {
			return fmt.Errorf("%s.weight: weight must be an integer", path)
		}
		var weight int64
		if errDecode := value.Decode(&weight); errDecode != nil {
			return fmt.Errorf("%s.weight: weight must be an integer", path)
		}
		if _, errNormalize := credentialweight.Normalize(weight); errNormalize != nil {
			return fmt.Errorf("%s.weight: %w", path, errNormalize)
		}
	}
	return nil
}

func validateOpenAICompatibilityWeightNodes(sequence *yaml.Node) error {
	if sequence == nil || sequence.Kind != yaml.SequenceNode {
		return nil
	}
	for providerIndex, provider := range sequence.Content {
		if provider == nil || provider.Kind != yaml.MappingNode {
			continue
		}
		for index := 0; index+1 < len(provider.Content); index += 2 {
			if provider.Content[index].Value != "api-key-entries" {
				continue
			}
			path := fmt.Sprintf("openai-compatibility[%d].api-key-entries", providerIndex)
			if errValidate := validateWeightSequenceNode(provider.Content[index+1], path); errValidate != nil {
				return errValidate
			}
		}
	}
	return nil
}

// ValidateCredentialWeights 校验每个 API-key 家族的权重。
func (cfg *Config) ValidateCredentialWeights() error {
	if cfg == nil {
		return nil
	}
	for index := range cfg.OpenAIKey {
		if errValidate := ValidateCredentialWeight(cfg.OpenAIKey[index].Weight); errValidate != nil {
			return fmt.Errorf("openai-api-key[%d].weight: %w", index, errValidate)
		}
	}
	for index := range cfg.DeepseekKey {
		if errValidate := ValidateCredentialWeight(cfg.DeepseekKey[index].Weight); errValidate != nil {
			return fmt.Errorf("deepseek-api-key[%d].weight: %w", index, errValidate)
		}
	}
	for index := range cfg.NvidiaKey {
		if errValidate := ValidateCredentialWeight(cfg.NvidiaKey[index].Weight); errValidate != nil {
			return fmt.Errorf("nvidia-api-key[%d].weight: %w", index, errValidate)
		}
	}
	for index := range cfg.AgnesKey {
		if errValidate := ValidateCredentialWeight(cfg.AgnesKey[index].Weight); errValidate != nil {
			return fmt.Errorf("agnes-api-key[%d].weight: %w", index, errValidate)
		}
	}
	for providerIndex := range cfg.OpenAICompatibility {
		for keyIndex := range cfg.OpenAICompatibility[providerIndex].APIKeyEntries {
			weight := cfg.OpenAICompatibility[providerIndex].APIKeyEntries[keyIndex].Weight
			if errValidate := ValidateCredentialWeight(weight); errValidate != nil {
				return fmt.Errorf("openai-compatibility[%d].api-key-entries[%d].weight: %w", providerIndex, keyIndex, errValidate)
			}
		}
	}
	return nil
}
