package agentschema

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// normalizeValue converts v to the canonical type of f (string for select and
// text, bool, float64 for number) and checks it against the field's rules.
func normalizeValue(f Field, v any) (any, error) {
	switch f.Type {
	case TypeBool:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("must be true or false, got %T", v)
		}
		return b, nil
	case TypeNumber:
		n, err := toFloat(v)
		if err != nil {
			return nil, err
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("must be a finite number")
		}
		if n < 0 {
			// "-1" would reach the CLI as an argument beginning with '-',
			// which it could read as another option.
			return nil, fmt.Errorf("%v must not be negative", n)
		}
		if f.Min != nil && n < *f.Min {
			return nil, fmt.Errorf("%v is below the minimum %v", n, *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return nil, fmt.Errorf("%v is above the maximum %v", n, *f.Max)
		}
		return n, nil
	case TypeSelect, TypeText:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("must be a string, got %T", v)
		}
		s = strings.TrimSpace(s)
		max := MaxTextValueLen
		if f.Type == TypeSelect {
			max = MaxOptionLen
		}
		if err := checkArgValue(s, max); err != nil {
			return nil, err
		}
		if strings.EqualFold(s, "bypassPermissions") {
			return nil, fmt.Errorf("%q is never passed to an agent: it removes every permission check", s)
		}
		if f.Type == TypeSelect && s != "" && !f.AllowCustom && !contains(f.Options, s) {
			return nil, fmt.Errorf("%q is not one of: %s", s, strings.Join(f.Options, ", "))
		}
		return s, nil
	default:
		return nil, fmt.Errorf("unknown field type %q", f.Type)
	}
}

func toFloat(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int8:
		return float64(n), nil
	case int16:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case uint:
		return float64(n), nil
	case uint8:
		return float64(n), nil
	case uint16:
		return float64(n), nil
	case uint32:
		return float64(n), nil
	case uint64:
		return float64(n), nil
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, fmt.Errorf("must be a number: %w", err)
		}
		return f, nil
	default:
		return 0, fmt.Errorf("must be a number, got %T", v)
	}
}

func contains(list []string, v string) bool {
	for _, o := range list {
		if o == v {
			return true
		}
	}
	return false
}

// ValidateValue checks one value for the field with key and returns it in its
// canonical type.
func (s *Schema) ValidateValue(key string, v any) (any, error) {
	f, ok := s.Field(key)
	if !ok {
		return nil, fmt.Errorf("agentschema: %s has no field %q", s.Name, key)
	}
	out, err := normalizeValue(f, v)
	if err != nil {
		return nil, fmt.Errorf("agentschema: %s.%s: %w", s.Name, key, err)
	}
	return out, nil
}

// ValidateValues checks every value against the schema and returns them in
// their canonical types. A key the schema does not declare is an error, so a
// misspelt option never silently does nothing.
func (s *Schema) ValidateValues(values map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(values))
	for _, key := range sortedKeys(values) {
		v, err := s.ValidateValue(key, values[key])
		if err != nil {
			return nil, err
		}
		out[key] = v
	}
	return out, nil
}

// Effective returns the value a field takes: the operator's value when set,
// the field's default otherwise, and nil when neither is.
func (f Field) Effective(values map[string]any) any {
	if v, ok := values[f.Key]; ok && v != nil {
		return v
	}
	return f.Default
}

// BuildArgs turns a custom agent's values into the flags passed to its CLI,
// in schema order. values must have passed ValidateValues. The rules:
//
//   - select and text: "<flag> <value>" when the value is not empty;
//   - number: "<flag> <value>" when the value is not zero (0 means "not set",
//     as Heimdallm's other numeric agent settings do);
//   - bool: "<flag>" when on, "<false_flag>" when off and the field has one.
//
// Values travel as separate argv entries; checkArgValue already refused any
// that begins with '-' and could be read as another option.
func (s *Schema) BuildArgs(values map[string]any) ([]string, error) {
	var args []string
	for _, f := range s.Fields {
		raw := f.Effective(values)
		if raw == nil {
			continue
		}
		v, err := normalizeValue(f, raw)
		if err != nil {
			return nil, fmt.Errorf("agentschema: %s.%s: %w", s.Name, f.Key, err)
		}
		switch f.Type {
		case TypeBool:
			switch {
			case v == true && f.Flag != "":
				args = append(args, f.Flag)
			case v == false && f.FalseFlag != "":
				args = append(args, f.FalseFlag)
			}
		case TypeNumber:
			n := v.(float64)
			if n != 0 && f.Flag != "" {
				args = append(args, f.Flag, strconv.FormatFloat(n, 'f', -1, 64))
			}
		default:
			if str := v.(string); str != "" && f.Flag != "" {
				args = append(args, f.Flag, str)
			}
		}
	}
	return args, nil
}

// Flags lists every flag the schema sets, so the free-form extra flags can be
// kept from overriding a validated field.
func (s *Schema) Flags() []string {
	var out []string
	for _, f := range s.Fields {
		for _, flag := range []string{f.Flag, f.FalseFlag} {
			if flag != "" {
				out = append(out, flag)
			}
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
