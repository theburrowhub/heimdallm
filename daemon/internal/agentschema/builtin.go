package agentschema

import (
	"embed"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
)

// The built-in schemas ship inside the daemon, so an upgrade updates them
// (new models, new options) without touching the values the operator stored
// in config.toml. They describe the typed settings of each built-in agent:
// every key is a CLIAgentConfig TOML key, and the executor still builds the
// agent's command line itself.
//
//go:embed builtin/*.json
var builtinFS embed.FS

var (
	builtinOnce    sync.Once
	builtinSchemas map[string]*Schema
	builtinErr     error
)

func loadBuiltins() {
	builtinSchemas = map[string]*Schema{}
	entries, err := builtinFS.ReadDir("builtin")
	if err != nil {
		builtinErr = err
		return
	}
	for _, e := range entries {
		data, err := builtinFS.ReadFile(path.Join("builtin", e.Name()))
		if err != nil {
			builtinErr = err
			return
		}
		s, err := Parse(data)
		if err != nil {
			builtinErr = fmt.Errorf("%s: %w", e.Name(), err)
			return
		}
		if err := s.Validate(); err != nil {
			builtinErr = fmt.Errorf("%s: %w", e.Name(), err)
			return
		}
		if want := strings.TrimSuffix(e.Name(), ".json"); s.Name != want {
			builtinErr = fmt.Errorf("%s: schema name %q does not match its file", e.Name(), s.Name)
			return
		}
		builtinSchemas[s.Name] = s
	}
}

// Builtin returns a copy of a built-in agent's schema.
func Builtin(name string) (*Schema, bool) {
	builtinOnce.Do(loadBuiltins)
	s, ok := builtinSchemas[name]
	if !ok {
		return nil, false
	}
	return s.Clone(), true
}

// Builtins returns a copy of every built-in schema, sorted by name.
func Builtins() []*Schema {
	builtinOnce.Do(loadBuiltins)
	names := make([]string, 0, len(builtinSchemas))
	for name := range builtinSchemas {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*Schema, 0, len(names))
	for _, name := range names {
		out = append(out, builtinSchemas[name].Clone())
	}
	return out
}

// BuiltinError reports a broken embedded schema. It is nil in any build that
// passed the package tests; the daemon checks it at startup anyway.
func BuiltinError() error {
	builtinOnce.Do(loadBuiltins)
	return builtinErr
}
