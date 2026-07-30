package config

import (
	"encoding"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
)

var (
	configSchemaType   = reflect.TypeOf(Config{})
	cliAgentSchemaType = reflect.TypeOf(CLIAgentConfig{})

	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
	tomlUnmarshalerType = reflect.TypeOf((*interface {
		UnmarshalTOML(any) error
	})(nil)).Elem()

	configSchemaValidation = sync.OnceValue(func() error {
		return validateKnownTOMLSchema(configSchemaType, "config", make(map[reflect.Type]bool))
	})
)

// projectKnownConfigMap returns a canonical, schema-shaped view of raw. It is
// intentionally not a generic TOML round-trip: unknown keys are omitted so an
// unrelated value that BurntSushi's encoder cannot represent (for example, a
// heterogeneous array) cannot make an otherwise valid legacy config fail to
// load.
func projectKnownConfigMap(raw map[string]any) (map[string]any, error) {
	if err := configSchemaValidation(); err != nil {
		return nil, err
	}
	projected, err := walkKnownTOMLValue(raw, configSchemaType, "config")
	if err != nil {
		return nil, err
	}
	out, ok := projected.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("config schema projection returned %T, want a table", projected)
	}
	return out, nil
}

func walkKnownTOMLValue(raw any, schema reflect.Type, path string) (any, error) {
	schema = indirectSchemaType(schema)
	if schema == nil {
		return nil, fmt.Errorf("config schema at %q has no concrete type", path)
	}
	if implementsTOMLScalar(schema) {
		return raw, nil
	}

	switch schema.Kind() {
	case reflect.Struct:
		table, ok := raw.(map[string]any)
		if !ok {
			// Keep the known value in the projection. The normal typed TOML
			// decoder will report the same useful type mismatch it did before.
			return raw, nil
		}
		return walkKnownTOMLStruct(table, schema, path)
	case reflect.Map:
		if schema.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("config schema map at %q must use string keys", path)
		}
		table, ok := raw.(map[string]any)
		if !ok {
			return raw, nil
		}
		projected := make(map[string]any, len(table))
		keys := sortedMapKeys(table)
		for _, key := range keys {
			// Map keys are user data (CLI names, orgs and repo slugs), not
			// schema identifiers. Preserve their spelling exactly.
			child, err := walkKnownTOMLValue(
				table[key],
				schema.Elem(),
				fmt.Sprintf("%s[%q]", path, key),
			)
			if err != nil {
				return nil, err
			}
			projected[key] = child
		}
		return projected, nil
	case reflect.Slice, reflect.Array:
		if err := validateHomogeneousTOMLArrays(raw, path, schema); err != nil {
			return nil, err
		}
		return raw, nil
	case reflect.Interface:
		return nil, fmt.Errorf("config schema at %q uses unsupported interface type %s", path, schema)
	default:
		return raw, nil
	}
}

func walkKnownTOMLStruct(table map[string]any, schema reflect.Type, path string) (map[string]any, error) {
	projected := make(map[string]any)
	for i := 0; i < schema.NumField(); i++ {
		field := schema.Field(i)
		if field.PkgPath != "" {
			continue
		}
		if field.Anonymous {
			return nil, fmt.Errorf("config schema at %q has unsupported anonymous field %s", path, field.Name)
		}

		canonical, include, err := canonicalTOMLFieldName(field)
		if err != nil {
			return nil, fmt.Errorf("config schema at %q: %w", path, err)
		}
		if !include {
			continue
		}

		matches := caseFoldedMapKeys(table, canonical)
		if len(matches) == 0 {
			continue
		}

		if schema == cliAgentSchemaType && canonical == "dangerously_skip_perms" {
			value, err := resolveFailClosedBool(table, matches, canonical)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			projected[canonical] = value
			continue
		}

		if isStructuralSchemaType(field.Type) && len(matches) > 1 {
			return nil, fmt.Errorf(
				"ambiguous structural aliases at %q for %q (%s); keep a single canonical %q key",
				path, canonical, strings.Join(matches, ", "), canonical,
			)
		}

		selected, discarded, err := selectCanonicalMapKey(matches, canonical)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		warnDiscardedAliases(path, canonical, discarded)
		original := table[selected]
		child, err := walkKnownTOMLValue(original, field.Type, path+"."+canonical)
		if err != nil {
			return nil, err
		}
		projected[canonical] = child
	}
	return projected, nil
}

func canonicalTOMLFieldName(field reflect.StructField) (name string, include bool, err error) {
	tag := strings.Split(field.Tag.Get("toml"), ",")[0]
	switch tag {
	case "-":
		return "", false, nil
	case "":
		return "", false, fmt.Errorf("exported field %s is missing a toml tag", field.Name)
	default:
		return tag, true, nil
	}
}

func caseFoldedMapKeys(table map[string]any, canonical string) []string {
	matches := make([]string, 0, 1)
	for key := range table {
		if strings.EqualFold(key, canonical) {
			matches = append(matches, key)
		}
	}
	sort.Strings(matches)
	return matches
}

func selectCanonicalMapKey(matches []string, canonical string) (string, []string, error) {
	for _, key := range matches {
		if key == canonical {
			discarded := make([]string, 0, len(matches)-1)
			for _, alias := range matches {
				if alias != canonical {
					discarded = append(discarded, alias)
				}
			}
			return canonical, discarded, nil
		}
	}
	if len(matches) == 1 {
		return matches[0], nil, nil
	}
	return "", nil, fmt.Errorf(
		"ambiguous aliases for %q (%s); keep a single canonical %q key",
		canonical, strings.Join(matches, ", "), canonical,
	)
}

func resolveFailClosedBool(table map[string]any, matches []string, canonical string) (bool, error) {
	value := true
	for _, key := range matches {
		candidate, ok := table[key].(bool)
		if !ok {
			return false, fmt.Errorf("%s must be a boolean", canonical)
		}
		if !candidate {
			value = false
		}
	}
	return value, nil
}

func warnDiscardedAliases(path, canonical string, discarded []string) {
	if len(discarded) == 0 {
		return
	}
	slog.Warn(
		"config: ignored case-variant aliases because the canonical key is present",
		"path", path,
		"field", canonical,
		"aliases", discarded,
	)
}

func validateHomogeneousTOMLArrays(raw any, path string, schema reflect.Type) error {
	if table, ok := raw.(map[string]any); ok {
		for _, key := range sortedMapKeys(table) {
			if err := validateHomogeneousTOMLArrays(
				table[key],
				fmt.Sprintf("%s[%q]", path, key),
				schema,
			); err != nil {
				return err
			}
		}
		return nil
	}

	value := reflect.ValueOf(raw)
	if !value.IsValid() || (value.Kind() != reflect.Slice && value.Kind() != reflect.Array) {
		return nil
	}

	var firstType reflect.Type
	firstIndex := -1
	childSchema := schema
	if schema.Kind() == reflect.Slice || schema.Kind() == reflect.Array {
		childSchema = schema.Elem()
	}
	for i := 0; i < value.Len(); i++ {
		element := value.Index(i).Interface()
		elementType := reflect.TypeOf(element)
		if firstIndex < 0 {
			firstType = elementType
			firstIndex = i
		} else if elementType != firstType {
			return fmt.Errorf(
				"%s: TOML array has mixed element types %s at index %d and %s at index %d; expected %s",
				path,
				displayType(firstType), firstIndex,
				displayType(elementType), i,
				schema,
			)
		}
		if err := validateHomogeneousTOMLArrays(
			element,
			fmt.Sprintf("%s[%d]", path, i),
			childSchema,
		); err != nil {
			return err
		}
	}
	return nil
}

func displayType(value reflect.Type) string {
	if value == nil {
		return "<nil>"
	}
	return value.String()
}

func sortedMapKeys(table map[string]any) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func indirectSchemaType(schema reflect.Type) reflect.Type {
	for schema != nil && schema.Kind() == reflect.Pointer {
		schema = schema.Elem()
	}
	return schema
}

func isStructuralSchemaType(schema reflect.Type) bool {
	schema = indirectSchemaType(schema)
	if schema == nil || implementsTOMLScalar(schema) {
		return false
	}
	return schema.Kind() == reflect.Struct || schema.Kind() == reflect.Map
}

func implementsTOMLScalar(schema reflect.Type) bool {
	if schema.Implements(textUnmarshalerType) || schema.Implements(tomlUnmarshalerType) {
		return true
	}
	return schema.Kind() != reflect.Pointer &&
		(reflect.PointerTo(schema).Implements(textUnmarshalerType) ||
			reflect.PointerTo(schema).Implements(tomlUnmarshalerType))
}

func validateKnownTOMLSchema(schema reflect.Type, path string, visiting map[reflect.Type]bool) error {
	schema = indirectSchemaType(schema)
	if schema == nil {
		return fmt.Errorf("config schema at %q has no concrete type", path)
	}
	if implementsTOMLScalar(schema) {
		return nil
	}

	switch schema.Kind() {
	case reflect.Struct:
		if visiting[schema] {
			return fmt.Errorf("config schema at %q contains recursive type %s", path, schema)
		}
		visiting[schema] = true
		defer delete(visiting, schema)
		fieldNames := make([]string, 0, schema.NumField())
		for i := 0; i < schema.NumField(); i++ {
			field := schema.Field(i)
			if field.PkgPath != "" {
				continue
			}
			if field.Anonymous {
				return fmt.Errorf("config schema at %q has unsupported anonymous field %s", path, field.Name)
			}
			canonical, include, err := canonicalTOMLFieldName(field)
			if err != nil {
				return fmt.Errorf("config schema at %q: %w", path, err)
			}
			if !include {
				continue
			}
			for _, existing := range fieldNames {
				if strings.EqualFold(existing, canonical) {
					return fmt.Errorf(
						"config schema at %q has case-insensitive duplicate TOML fields %q and %q",
						path, existing, canonical,
					)
				}
			}
			fieldNames = append(fieldNames, canonical)
			if err := validateKnownTOMLSchema(field.Type, path+"."+canonical, visiting); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if schema.Key().Kind() != reflect.String {
			return fmt.Errorf("config schema map at %q must use string keys", path)
		}
		return validateKnownTOMLSchema(schema.Elem(), path+"[*]", visiting)
	case reflect.Slice, reflect.Array:
		element := indirectSchemaType(schema.Elem())
		if element == nil || implementsTOMLScalar(element) {
			return nil
		}
		switch element.Kind() {
		case reflect.Bool,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64,
			reflect.String:
			return nil
		default:
			return fmt.Errorf(
				"config schema at %q uses unsupported composite %s elements",
				path, element,
			)
		}
	case reflect.Interface:
		return fmt.Errorf("config schema at %q uses unsupported interface type %s", path, schema)
	default:
		return nil
	}
}
