package config

import (
	"reflect"
	"strings"
	"testing"
)

type projectionTextScalar struct{}

func (*projectionTextScalar) UnmarshalText([]byte) error { return nil }

type projectionTOMLScalar struct{}

func (*projectionTOMLScalar) UnmarshalTOML(any) error { return nil }

type ProjectionEmbedded struct{}

func TestValidateKnownTOMLSchema_RejectsUnsupportedShapes(t *testing.T) {
	tests := []struct {
		name   string
		schema reflect.Type
		marker string
	}{
		{
			name: "case-insensitive duplicate tags",
			schema: reflect.TypeOf(struct {
				First  string `toml:"field"`
				Second string `toml:"FIELD"`
			}{}),
			marker: `duplicate TOML fields "field" and "FIELD"`,
		},
		{
			name: "anonymous field",
			schema: reflect.TypeOf(struct {
				ProjectionEmbedded
			}{}),
			marker: "unsupported anonymous field",
		},
		{
			name: "interface field",
			schema: reflect.TypeOf(struct {
				Value any `toml:"value"`
			}{}),
			marker: "unsupported interface type",
		},
		{
			name: "composite slice elements",
			schema: reflect.TypeOf(struct {
				Values []struct {
					Name string `toml:"name"`
				} `toml:"values"`
			}{}),
			marker: "unsupported composite",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateKnownTOMLSchema(tc.schema, "test", make(map[reflect.Type]bool))
			if err == nil || !strings.Contains(err.Error(), tc.marker) {
				t.Fatalf("schema error = %v, want marker %q", err, tc.marker)
			}
		})
	}
}

func TestValidateKnownTOMLSchema_TreatsCustomUnmarshalersAsScalars(t *testing.T) {
	schema := reflect.TypeOf(struct {
		Text projectionTextScalar `toml:"text"`
		TOML projectionTOMLScalar `toml:"toml"`
	}{})

	if err := validateKnownTOMLSchema(schema, "test", make(map[reflect.Type]bool)); err != nil {
		t.Fatalf("custom scalar schema rejected: %v", err)
	}
}

func TestFailClosedDangerousValue_FalseWinsEveryAliasShape(t *testing.T) {
	tests := []struct {
		name  string
		flags map[string]any
		want  bool
	}{
		{
			name: "canonical true",
			flags: map[string]any{
				"dangerously_skip_perms": true,
			},
			want: true,
		},
		{
			name: "canonical false beats alias true",
			flags: map[string]any{
				"dangerously_skip_perms": false,
				"DANGEROUSLY_SKIP_PERMS": true,
			},
			want: false,
		},
		{
			name: "alias false beats canonical true",
			flags: map[string]any{
				"dangerously_skip_perms": true,
				"DANGEROUSLY_SKIP_PERMS": false,
			},
			want: false,
		},
		{
			name: "alias false beats another alias",
			flags: map[string]any{
				"Dangerously_Skip_Perms": true,
				"DANGEROUSLY_SKIP_PERMS": false,
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			matches := caseFoldedMapKeys(tc.flags, "dangerously_skip_perms")
			got, err := failClosedDangerousValue(tc.flags, matches)
			if err != nil {
				t.Fatalf("failClosedDangerousValue: %v", err)
			}
			if got != tc.want {
				t.Fatalf("dangerous value = %v, want %v", got, tc.want)
			}
		})
	}

	flags := map[string]any{"DANGEROUSLY_SKIP_PERMS": "false"}
	if _, err := failClosedDangerousValue(
		flags,
		caseFoldedMapKeys(flags, "dangerously_skip_perms"),
	); err == nil || !strings.Contains(err.Error(), "must be a boolean") {
		t.Fatalf("non-boolean dangerous alias error = %v", err)
	}
}
