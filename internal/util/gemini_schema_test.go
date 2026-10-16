package util

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func compareJSON(t *testing.T, expectedJSON, actualJSON string) {
	var expMap, actMap map[string]interface{}
	errExp := json.Unmarshal([]byte(expectedJSON), &expMap)
	errAct := json.Unmarshal([]byte(actualJSON), &actMap)

	if errExp != nil || errAct != nil {
		t.Fatalf("JSON Unmarshal error. Exp: %v, Act: %v", errExp, errAct)
	}

	if !reflect.DeepEqual(expMap, actMap) {
		expBytes, _ := json.MarshalIndent(expMap, "", "  ")
		actBytes, _ := json.MarshalIndent(actMap, "", "  ")
		t.Errorf("JSON mismatch:\nExpected:\n%s\n\nActual:\n%s", string(expBytes), string(actBytes))
	}
}

// ============================================================================
// Empty Schema Placeholder Tests
// ============================================================================

// ============================================================================
// Format field handling (ad-hoc patch removal)
// ============================================================================

func TestRemoveExtensionFields(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "removes x- fields at root",
			input: `{
				"type": "object",
				"x-custom-meta": "value",
				"properties": {
					"foo": { "type": "string" }
				}
			}`,
			expected: `{
				"type": "object",
				"properties": {
					"foo": { "type": "string" }
				}
			}`,
		},
		{
			name: "removes x- fields in nested properties",
			input: `{
				"type": "object",
				"properties": {
					"foo": {
						"type": "string",
						"x-internal-id": 123
					}
				}
			}`,
			expected: `{
				"type": "object",
				"properties": {
					"foo": {
						"type": "string"
					}
				}
			}`,
		},
		{
			name: "does NOT remove properties named x-",
			input: `{
				"type": "object",
				"properties": {
					"x-data": { "type": "string" },
					"normal": { "type": "number", "x-meta": "remove" }
				},
				"required": ["x-data"]
			}`,
			expected: `{
				"type": "object",
				"properties": {
					"x-data": { "type": "string" },
					"normal": { "type": "number" }
				},
				"required": ["x-data"]
			}`,
		},
		{
			name: "does NOT remove $schema and other meta fields (as requested)",
			input: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$id": "test",
				"type": "object",
				"properties": {
					"foo": { "type": "string" }
				}
			}`,
			expected: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$id": "test",
				"type": "object",
				"properties": {
					"foo": { "type": "string" }
				}
			}`,
		},
		{
			name: "handles properties named $schema",
			input: `{
				"type": "object",
				"properties": {
					"$schema": { "type": "string" }
				}
			}`,
			expected: `{
				"type": "object",
				"properties": {
					"$schema": { "type": "string" }
				}
			}`,
		},
		{
			name: "handles escaping in paths",
			input: `{
				"type": "object",
				"properties": {
					"foo.bar": {
						"type": "string",
						"x-meta": "remove"
					}
				},
				"x-root.meta": "remove"
			}`,
			expected: `{
				"type": "object",
				"properties": {
					"foo.bar": {
						"type": "string"
					}
				}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := removeExtensionFields(tt.input)
			compareJSON(t, tt.expected, actual)
		})
	}
}

// TestIsPropertyDefinitionDistinguishesPropertyNamedProperties covers the classification that
// decides whether a key spelled like a schema keyword is a keyword or an author-chosen name.
// Matching a trailing ".properties" alone mistook the schema of a property named "properties" for
// a property map, which disabled cleaning inside it.
func TestIsPropertyDefinitionDistinguishesPropertyNamedProperties(t *testing.T) {
	for path, want := range map[string]bool{
		"":                                    false,
		"properties":                          true,
		"properties.properties":               false,
		"properties.properties.properties":    true,
		"properties.records.items.properties": true,
		"properties.records.items":            false,
		// Any prefix the caller nests the schema under must not change the answer.
		"schema.properties": true,
		"request.tools.0.functionDeclarations.0.parameters":                       false,
		"request.tools.0.functionDeclarations.0.parameters.properties":            true,
		"request.tools.0.functionDeclarations.0.parameters.properties.properties": false,
		// $defs and patternProperties are name maps for the same reason as properties.
		"$defs":                          true,
		"$defs.properties":               false,
		"properties.$defs":               false,
		"properties.a.patternProperties": true,
		"properties.patternProperties":   false,
	} {
		if got := isPropertyDefinition(path); got != want {
			t.Errorf("isPropertyDefinition(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestSortByDepthUsesSegmentsAndIsStable(t *testing.T) {
	paths := []string{"root.verylong", "root.x.y", "first.same", "later.same"}
	sortByDepth(paths)
	want := []string{"root.x.y", "root.verylong", "first.same", "later.same"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("sortByDepth() = %v, want %v", paths, want)
	}
}

func TestCleanJSONSchemaForGeminiJSONSchema_PreservesAdditionalPropertiesAndPattern_Issue5959(t *testing.T) {
	input := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"title": "SubmitTool",
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"recipient": {
				"type": "string",
				"pattern": "^(alice|bob)$",
				"minLength": 3,
				"maxLength": 10
			},
			"amount": {
				"type": "number",
				"minimum": 1
			},
			"nested": {
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"tag": {
						"type": "string",
						"pattern": "^[a-z]+$"
					}
				}
			},
			"items_list": {
				"type": "array",
				"items": {
					"type": "string",
					"pattern": "^[0-9]+$"
				}
			}
		},
		"required": ["recipient", "amount", "non_existent"]
	}`

	cleaned := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(cleaned)

	if got := parsed.Get("additionalProperties"); !got.Exists() || got.Type != gjson.False {
		t.Fatalf("additionalProperties should be preserved as false, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.recipient.pattern"); !got.Exists() || got.String() != "^(alice|bob)$" {
		t.Fatalf("pattern should be preserved, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.recipient.minLength").Int(); got != 3 {
		t.Fatalf("minLength should be preserved as 3, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.recipient.maxLength").Int(); got != 10 {
		t.Fatalf("maxLength should be preserved as 10, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.amount.minimum").Int(); got != 1 {
		t.Fatalf("minimum should be preserved as 1, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.nested.additionalProperties"); !got.Exists() || got.Type != gjson.False {
		t.Fatalf("nested additionalProperties should be preserved as false, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.nested.properties.tag.pattern"); !got.Exists() || got.String() != "^[a-z]+$" {
		t.Fatalf("nested tag pattern should be preserved, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.items_list.items.pattern"); !got.Exists() || got.String() != "^[0-9]+$" {
		t.Fatalf("array items pattern should be preserved, got: %v. Cleaned: %s", got, cleaned)
	}
	if parsed.Get("title").Exists() {
		t.Fatalf("title should be removed. Cleaned: %s", cleaned)
	}
	if parsed.Get("$schema").Exists() {
		t.Fatalf("$schema should be removed. Cleaned: %s", cleaned)
	}
	if parsed.Get("description").Exists() && parsed.Get("description").String() == "No extra properties allowed" {
		t.Fatalf("additionalProperties: false should not be converted to description hint. Cleaned: %s", cleaned)
	}
	if got := parsed.Get("properties.recipient.description"); got.Exists() && strings.Contains(got.String(), "pattern:") {
		t.Fatalf("pattern should not be converted to description hint. Cleaned: %s", cleaned)
	}
	if got := parsed.Get("properties.nested.properties.tag.description"); got.Exists() && strings.Contains(got.String(), "pattern:") {
		t.Fatalf("nested pattern should not be converted to description hint. Cleaned: %s", cleaned)
	}
	if got := parsed.Get("properties.items_list.items.description"); got.Exists() && strings.Contains(got.String(), "pattern:") {
		t.Fatalf("array items pattern should not be converted to description hint. Cleaned: %s", cleaned)
	}
	// non_existent should be removed from required because it's not in properties
	required := parsed.Get("required").Array()
	if len(required) != 2 {
		t.Fatalf("required length = %d, want 2. Cleaned: %s", len(required), cleaned)
	}
}

func TestCleanJSONSchemaForGeminiJSONSchema_PreservesSchemaValuedAdditionalProperties(t *testing.T) {
	input := `{
		"type": "object",
		"additionalProperties": {
			"type": "string",
			"pattern": "^[a-z]+$",
			"minLength": 2
		}
	}`

	cleaned := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(cleaned)

	ap := parsed.Get("additionalProperties")
	if !ap.Exists() || !ap.IsObject() {
		t.Fatalf("additionalProperties object should be preserved, got: %s", cleaned)
	}
	if got := ap.Get("type").String(); got != "string" {
		t.Fatalf("additionalProperties.type = %q, want string", got)
	}
	if got := ap.Get("pattern").String(); got != "^[a-z]+$" {
		t.Fatalf("additionalProperties.pattern = %q, want ^[a-z]+$", got)
	}
	if got := ap.Get("minLength").Int(); got != 2 {
		t.Fatalf("additionalProperties.minLength = %d, want 2", got)
	}
	if ap.Get("description").Exists() && strings.Contains(ap.Get("description").String(), "pattern:") {
		t.Fatalf("additionalProperties pattern should not be converted to description hint: %s", cleaned)
	}
}

// TestCleanJSONSchema_ArrayItemsRequireArrayType_Issue6011 covers Issue #6011:
// Gemini rejects tool schemas where a property declares "items" but its type is not "array"
// (e.g. type is omitted or flattened to a non-array type), returning:
// "field predicate failed: $type == Type.ARRAY".
func TestCleanJSONSchema_ArrayItemsRequireArrayType_Issue6011(t *testing.T) {
	// Case 1: property declares "items" but omits "type": "array" (relying on JSON Schema inference)
	// Reproduces the error path reported in Issue #6011: properties.revision_reasons.items.properties.evidence_reference
	inputMissingType := `{
		"type": "object",
		"properties": {
			"revision_reasons": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"evidence_reference": {
							"description": "references to evidence",
							"items": {
								"type": "string"
							}
						}
					}
				}
			}
		}
	}`

	cleaners := map[string]func(string) string{
		"geminiJSONSchema": CleanJSONSchemaForGeminiJSONSchema,
	}

	for name, clean := range cleaners {
		t.Run(name+"_missing_type", func(t *testing.T) {
			cleaned := clean(inputMissingType)
			parsed := gjson.Parse(cleaned)
			target := parsed.Get("properties.revision_reasons.items.properties.evidence_reference")
			if !target.Exists() {
				t.Fatalf("evidence_reference property missing in %s: %s", name, cleaned)
			}
			if gotType := target.Get("type").String(); gotType != "array" {
				t.Fatalf("%s: evidence_reference.type = %q, want 'array'; cleaned schema: %s", name, gotType, cleaned)
			}
			if gotItemsType := target.Get("items.type").String(); gotItemsType != "string" {
				t.Fatalf("%s: evidence_reference.items.type = %q, want 'string'; cleaned schema: %s", name, gotItemsType, cleaned)
			}
		})
	}

	// Case 2: property declares union type: ["string", "array"] with items
	inputUnionType := `{
		"type": "object",
		"properties": {
			"revision_reasons": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"evidence_reference": {
							"type": ["string", "array"],
							"items": {
								"type": "string"
							}
						}
					}
				}
			}
		}
	}`

	for name, clean := range cleaners {
		t.Run(name+"_union_type_with_items", func(t *testing.T) {
			cleaned := clean(inputUnionType)
			parsed := gjson.Parse(cleaned)
			target := parsed.Get("properties.revision_reasons.items.properties.evidence_reference")
			if gotType := target.Get("type").String(); gotType != "array" {
				t.Fatalf("%s: evidence_reference.type = %q, want 'array' when items is defined; cleaned schema: %s", name, gotType, cleaned)
			}
		})
	}

	// Case 3: property has explicit non-array type (e.g. "string") with extraneous items
	inputNonArrayWithItems := `{
		"type": "object",
		"properties": {
			"label": {
				"type": "string",
				"items": { "type": "string" }
			},
			"config": {
				"type": "object",
				"properties": { "key": { "type": "string" } },
				"items": { "type": "string" }
			}
		}
	}`

	for name, clean := range cleaners {
		t.Run(name+"_non_array_strips_items", func(t *testing.T) {
			cleaned := clean(inputNonArrayWithItems)
			parsed := gjson.Parse(cleaned)
			if parsed.Get("properties.label.items").Exists() {
				t.Fatalf("%s: properties.label should not retain items on string type: %s", name, cleaned)
			}
			if parsed.Get("properties.config.items").Exists() {
				t.Fatalf("%s: properties.config should not retain items on object type: %s", name, cleaned)
			}
			if parsed.Get("properties.label.type").String() != "string" {
				t.Fatalf("%s: properties.label.type should remain string: %s", name, cleaned)
			}
		})
	}

	// Case 4: property literally named "items" should be preserved and not treated as array items
	inputPropertyNamedItems := `{
		"type": "object",
		"properties": {
			"items": {
				"type": "string",
				"description": "a field named items"
			}
		}
	}`

	for name, clean := range cleaners {
		t.Run(name+"_property_named_items_preserved", func(t *testing.T) {
			cleaned := clean(inputPropertyNamedItems)
			parsed := gjson.Parse(cleaned)
			itemProp := parsed.Get("properties.items")
			if !itemProp.Exists() {
				t.Fatalf("%s: property named 'items' was removed: %s", name, cleaned)
			}
			if itemProp.Get("type").String() != "string" {
				t.Fatalf("%s: property named 'items' type changed: %s", name, cleaned)
			}
		})
	}

	// Case 5: Root array with items missing type
	inputRootArrayMissingType := `{
		"items": {
			"type": "string"
		}
	}`

	for name, clean := range cleaners {
		t.Run(name+"_root_array_missing_type", func(t *testing.T) {
			cleaned := clean(inputRootArrayMissingType)
			parsed := gjson.Parse(cleaned)
			if parsed.Get("type").String() != "array" {
				t.Fatalf("%s: root schema type should be inferred as 'array': %s", name, cleaned)
			}
			if parsed.Get("items.type").String() != "string" {
				t.Fatalf("%s: root schema items.type should remain 'string': %s", name, cleaned)
			}
		})
	}

	// Case 6: Cleaning is idempotent across all cleaners
	for name, clean := range cleaners {
		t.Run(name+"_idempotent", func(t *testing.T) {
			cleaned1 := clean(inputMissingType)
			cleaned2 := clean(cleaned1)
			if cleaned1 != cleaned2 {
				t.Fatalf("%s: cleaner is not idempotent.\nFirst:\n%s\nSecond:\n%s", name, cleaned1, cleaned2)
			}
		})
	}
}

// TestCleanJSONSchema_EnforcesObjectTypeForProperties_Issue6394 tests issue 6394:
// When a property defines nested properties/required (e.g. through a union type like ["string", "object"],
// merged anyOf branches, or missing type), Gemini requires type to be OBJECT.
// Properties or required must never be emitted on a non-OBJECT type node.
func TestCleanJSONSchema_EnforcesObjectTypeForProperties_Issue6394(t *testing.T) {
	cleaners := map[string]func(string) string{
		"GeminiJSONSchema": CleanJSONSchemaForGeminiJSONSchema,
	}

	t.Run("TypeArrayWithPropertiesPrefersObject", func(t *testing.T) {
		input := `{"type":"object","properties":{"requestBody":{"type":"object","properties":{"delivery":{"type":"object","properties":{"endpoint":{"type":["string","object"],"properties":{"traces":{"type":"string"}},"required":["traces"]}},"required":["endpoint"]}},"required":["delivery"]}},"required":["requestBody"]}`

		for name, clean := range cleaners {
			t.Run(name, func(t *testing.T) {
				got := clean(input)
				parsed := gjson.Parse(got)

				endpointType := parsed.Get("properties.requestBody.properties.delivery.properties.endpoint.type").String()
				if !strings.EqualFold(endpointType, "object") {
					t.Fatalf("[%s] endpoint type = %q, want object; cleaned: %s", name, endpointType, got)
				}
				if !parsed.Get("properties.requestBody.properties.delivery.properties.endpoint.properties.traces").Exists() {
					t.Fatalf("[%s] endpoint.properties.traces missing; cleaned: %s", name, got)
				}
			})
		}
	})

	t.Run("AnyOfUnionMergedIntoNodeWithoutTypeEnsuresObject", func(t *testing.T) {
		input := `{"type":"object","properties":{"endpoint":{"properties":{"traces":{"type":"string"}},"required":["traces"],"anyOf":[{"type":"string"},{"type":"object","properties":{"other":{"type":"string"}}}]}}}`

		for name, clean := range cleaners {
			t.Run(name, func(t *testing.T) {
				got := clean(input)
				parsed := gjson.Parse(got)

				endpointType := parsed.Get("properties.endpoint.type").String()
				if !strings.EqualFold(endpointType, "object") {
					t.Fatalf("[%s] endpoint type = %q, want object; cleaned: %s", name, endpointType, got)
				}
				if !parsed.Get("properties.endpoint.properties.traces").Exists() {
					t.Fatalf("[%s] endpoint.properties.traces missing; cleaned: %s", name, got)
				}
			})
		}
	})

	t.Run("PrimitiveTypeWithPropertiesPromotedToObject", func(t *testing.T) {
		input := `{"type":"object","properties":{"endpoint":{"type":"string","properties":{"traces":{"type":"string"}},"required":["traces"]}}}`

		for name, clean := range cleaners {
			t.Run(name, func(t *testing.T) {
				got := clean(input)
				parsed := gjson.Parse(got)

				endpointType := parsed.Get("properties.endpoint.type").String()
				if !strings.EqualFold(endpointType, "object") {
					t.Fatalf("[%s] endpoint type = %q, want object; cleaned: %s", name, endpointType, got)
				}
			})
		}
	})
}

// TestCleanJSONSchema_RemovesDependentKeywords_Issue6539 covers Issue #6539:
// JSON Schema keywords dependentRequired, dependentSchemas, and dependencies are rejected by
// the Gemini/Antigravity backend with 400 INVALID_ARGUMENT ("Unknown name \"dependentRequired\" ... Cannot find field")
// and must be stripped while preserving legitimate tool properties with those names.
func TestCleanJSONSchema_RemovesDependentKeywords_Issue6539(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"baseline_url": {"type": "string"},
			"candidate_url": {"type": "string"},
			"options": {
				"type": "object",
				"properties": {
					"threshold": {"type": "number"},
					"diff_color": {"type": "string"}
				},
				"dependentRequired": {
					"threshold": ["diff_color"]
				},
				"dependentSchemas": {
					"threshold": {
						"properties": {
							"diff_color": {"type": "string"}
						}
					}
				},
				"dependencies": {
					"threshold": ["diff_color"]
				}
			},
			"dependentRequired": {
				"type": "string",
				"description": "Legitimate property named dependentRequired"
			},
			"dependentSchemas": {
				"type": "object",
				"properties": {
					"nested": {"type": "string"}
				},
				"dependentRequired": {
					"nested": ["other"]
				}
			},
			"dependencies": {
				"type": "string",
				"description": "Legitimate property named dependencies"
			}
		},
		"dependentRequired": {
			"baseline_url": ["candidate_url"]
		},
		"dependentSchemas": {
			"baseline_url": {
				"required": ["candidate_url"]
			}
		},
		"dependencies": {
			"baseline_url": ["candidate_url"]
		}
	}`

	cleaners := map[string]func(string) string{
		"GeminiJSONSchema": CleanJSONSchemaForGeminiJSONSchema,
	}

	for name, clean := range cleaners {
		t.Run(name, func(t *testing.T) {
			got := clean(input)
			parsed := gjson.Parse(got)

			for _, kw := range []string{"dependentRequired", "dependentSchemas", "dependencies"} {
				if parsed.Get(kw).Exists() {
					t.Errorf("[%s] root %q was not removed: %s", name, kw, got)
				}
				if parsed.Get("properties.options." + kw).Exists() {
					t.Errorf("[%s] nested properties.options.%s was not removed: %s", name, kw, got)
				}
			}

			if parsed.Get("properties.dependentSchemas.dependentRequired").Exists() {
				t.Errorf("[%s] properties.dependentSchemas.dependentRequired was not removed: %s", name, got)
			}

			for _, prop := range []string{"dependentRequired", "dependentSchemas", "dependencies"} {
				if !parsed.Get("properties." + prop).Exists() {
					t.Errorf("[%s] legitimate property %q was removed: %s", name, prop, got)
				}
			}
		})
	}
}

// TestCleanJSONSchema_PreservesMultiBranchAnyOfOneOf_Issue3662 covers Issue #3662:
// Multi-branch anyOf/oneOf unions (such as string | image object | audio object) must not be
// flattened to a single selected branch, which previously caused non-first object branches (e.g. audio)
// to disappear from the cleaned schema.
func TestCleanJSONSchema_PreservesMultiBranchAnyOfOneOf_Issue3662(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"item": {
				"anyOf": [
					{ "type": "string" },
					{
						"type": "object",
						"properties": {
							"image": { "type": "string" }
						},
						"required": ["image"]
					},
					{
						"type": "object",
						"properties": {
							"audio": { "type": "string" }
						},
						"required": ["audio"]
					}
				]
			}
		},
		"required": ["item"]
	}`

	cleaners := map[string]func(string) string{
		"GeminiJSONSchema": CleanJSONSchemaForGeminiJSONSchema,
	}

	for name, clean := range cleaners {
		t.Run(name, func(t *testing.T) {
			got := clean(input)
			compareJSON(t, input, got)

			// Verify idempotency when cleaned a second time
			gotSecond := clean(got)
			compareJSON(t, got, gotSecond)
		})
	}

	t.Run("NullableMultiBranchUnionStripsNullBranchAndSetsNullable", func(t *testing.T) {
		nullableUnion := `{
			"type": "object",
			"properties": {
				"item": {
					"anyOf": [
						{ "type": "string" },
						{
							"type": "object",
							"properties": { "image": { "type": "string" } },
							"required": ["image"]
						},
						{
							"type": "object",
							"properties": { "audio": { "type": "string" } },
							"required": ["audio"]
						},
						{ "type": "null" }
					]
				}
			},
			"required": ["item"]
		}`

		geminiCleaned := gjson.Parse(CleanJSONSchemaForGeminiJSONSchema(nullableUnion))
		if len(geminiCleaned.Get("properties.item.anyOf").Array()) != 3 {
			t.Fatalf("GeminiJSONSchema anyOf branch count = %d, want 3: %s", len(geminiCleaned.Get("properties.item.anyOf").Array()), geminiCleaned.Raw)
		}
		if geminiCleaned.Get("properties.item.nullable").Exists() {
			t.Fatalf("GeminiJSONSchema should not keep nullable keyword: %s", geminiCleaned.Raw)
		}
		if !strings.Contains(geminiCleaned.Get("properties.item.description").String(), "(nullable)") {
			t.Fatalf("GeminiJSONSchema missing (nullable) hint: %s", geminiCleaned.Raw)
		}

	})

	t.Run("OneOfNormalizedToAnyOfOnNativeSchemaCarriers", func(t *testing.T) {
		oneOfInput := `{
			"type": "object",
			"properties": {
				"item": {
					"oneOf": [
						{ "type": "string" },
						{
							"type": "object",
							"properties": { "image": { "type": "string" } },
							"required": ["image"]
						},
						{
							"type": "object",
							"properties": { "audio": { "type": "string" } },
							"required": ["audio"]
						}
					]
				}
			},
			"required": ["item"]
		}`

		// parametersJsonSchema carrier preserves standard JSON Schema oneOf
		jsonSchemaParsed := gjson.Parse(CleanJSONSchemaForGeminiJSONSchema(oneOfInput))
		if got := len(jsonSchemaParsed.Get("properties.item.oneOf").Array()); got != 3 {
			t.Fatalf("[GeminiJSONSchema] oneOf branch count = %d, want 3: %s", got, jsonSchemaParsed.Raw)
		}
	})

	t.Run("PreservesUnionAndBranchRequiredWhenParentHasProperties", func(t *testing.T) {
		parentPropsUnion := `{
			"type": "object",
			"properties": {},
			"anyOf": [
				{
					"type": "object",
					"properties": { "image": { "type": "string" } },
					"required": ["image"]
				},
				{
					"type": "object",
					"properties": { "audio": { "type": "string" } },
					"required": ["audio"]
				}
			]
		}`

		for name, clean := range cleaners {
			t.Run(name, func(t *testing.T) {
				got := clean(parentPropsUnion)
				compareJSON(t, parentPropsUnion, got)
			})
		}

	})

	t.Run("BooleanUnionBranchesAndSiblingAnyOfOneOf", func(t *testing.T) {
		// When a node declares both anyOf and oneOf, native Schema carriers keep anyOf without concatenating
		// oneOf into it and record oneOf as a description hint, while GeminiJSONSchema preserves both.
		siblingUnions := `{
			"type": "object",
			"properties": {
				"value": {
					"anyOf": [
						{ "type": "string" },
						{ "type": "integer" }
					],
					"oneOf": [
						{ "type": "string" },
						{ "type": "boolean" }
					]
				}
			}
		}`
		jsonSchemaSibling := gjson.Parse(CleanJSONSchemaForGeminiJSONSchema(siblingUnions))
		if len(jsonSchemaSibling.Get("properties.value.anyOf").Array()) != 2 || len(jsonSchemaSibling.Get("properties.value.oneOf").Array()) != 2 {
			t.Fatalf("[GeminiJSONSchema] expected both anyOf and oneOf to be preserved: %s", jsonSchemaSibling.Raw)
		}

		// Null-only unions (anyOf:[null], oneOf:[null], anyOf:[false, null]) must not panic across any cleaner.
		nullOnlyCleaners := map[string]func(string) string{
			"GeminiJSONSchema": CleanJSONSchemaForGeminiJSONSchema,
		}
		for _, nullOnlyInput := range []string{
			`{"type":"object","properties":{"value":{"anyOf":[{"type":"null"}]}}}`,
			`{"type":"object","properties":{"value":{"oneOf":[{"type":"null"},{"type":"null"}]}}}`,
			`{"type":"object","properties":{"value":{"anyOf":[false,{"type":"null"}],"description":"Null only"}}}`,
		} {
			for name, clean := range nullOnlyCleaners {
				got := clean(nullOnlyInput)
				if !gjson.Valid(got) {
					t.Fatalf("[%s] invalid JSON for null-only union %s: %s", name, nullOnlyInput, got)
				}
				if gjson.Get(got, "properties.value.type").String() != "null" {
					t.Fatalf("[%s] expected properties.value.type = null, got: %s", name, got)
				}
			}
		}
	})
}

// TestCleanJSONSchema_RemovesDiscriminatorKeyword_Issue6639 covers Issue #6639:
// OpenAPI/JSON Schema keyword "discriminator" alongside preserved anyOf/oneOf unions
// (both directly on a property schema and inside array items) or on object schemas
// is rejected by the Gemini/Antigravity backend with 400 INVALID_ARGUMENT
// ("Unknown name \"discriminator\" ... Cannot find field") and must be stripped while
// preserving legitimate tool properties named "discriminator".
func TestCleanJSONSchema_RemovesDiscriminatorKeyword_Issue6639(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"target": {
				"anyOf": [
					{"type": "object", "properties": {"kind": {"const": "a"}, "url": {"type": "string"}}},
					{"type": "object", "properties": {"kind": {"const": "b"}, "path": {"type": "string"}}}
				],
				"discriminator": {"propertyName": "kind"}
			},
			"deliverables": {
				"type": "array",
				"items": {
					"anyOf": [
						{"type": "object", "properties": {"kind": {"const": "file"}, "path": {"type": "string"}}},
						{"type": "object", "properties": {"kind": {"const": "link"}, "url": {"type": "string"}}}
					],
					"discriminator": {"propertyName": "kind"}
				}
			},
			"polymorphicObject": {
				"type": "object",
				"properties": {
					"kind": {"type": "string"}
				},
				"oneOf": [
					{"properties": {"a": {"type": "string"}}},
					{"properties": {"b": {"type": "number"}}}
				],
				"discriminator": {
					"propertyName": "kind",
					"mapping": {"a": "#/definitions/A", "b": "#/definitions/B"}
				}
			},
			"discriminator": {
				"type": "string",
				"description": "Legitimate property named discriminator"
			},
			"nestedDiscriminatorProp": {
				"type": "object",
				"properties": {
					"discriminator": {
						"type": "object",
						"properties": {
							"kind": {"type": "string"}
						},
						"discriminator": {"propertyName": "kind"}
					}
				}
			}
		},
		"discriminator": {"propertyName": "target"}
	}`

	cleaners := map[string]func(string) string{
		"GeminiJSONSchema": CleanJSONSchemaForGeminiJSONSchema,
	}

	for name, clean := range cleaners {
		t.Run(name, func(t *testing.T) {
			got := clean(input)
			parsed := gjson.Parse(got)

			for _, forbiddenPath := range []string{
				"discriminator",
				"properties.target.discriminator",
				"properties.deliverables.items.discriminator",
				"properties.polymorphicObject.discriminator",
				"properties.nestedDiscriminatorProp.properties.discriminator.discriminator",
			} {
				if parsed.Get(forbiddenPath).Exists() {
					t.Errorf("[%s] %s was not removed: %s", name, forbiddenPath, got)
				}
			}

			if name != "AntigravityClaudeTool" {
				if gotBranches := len(parsed.Get("properties.target.anyOf").Array()); gotBranches != 2 {
					t.Errorf("[%s] properties.target.anyOf branch count = %d, want 2: %s", name, gotBranches, got)
				}
				if gotBranches := len(parsed.Get("properties.deliverables.items.anyOf").Array()); gotBranches != 2 {
					t.Errorf("[%s] properties.deliverables.items.anyOf branch count = %d, want 2: %s", name, gotBranches, got)
				}
			}

			if !parsed.Get("properties.discriminator").Exists() {
				t.Errorf("[%s] legitimate property \"discriminator\" was removed: %s", name, got)
			}
			if !parsed.Get("properties.nestedDiscriminatorProp.properties.discriminator.properties.kind").Exists() {
				t.Errorf("[%s] legitimate nested property \"discriminator\" was removed: %s", name, got)
			}
		})
	}
}
