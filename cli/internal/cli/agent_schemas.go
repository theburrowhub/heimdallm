package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
	"github.com/theburrowhub/heimdallm/cli/internal/tui"
)

// maxSchemaFile bounds a schema file read by `agents add`, like the daemon.
const maxSchemaFile = 64 << 10

func newAgentSchemaCmds() []*cobra.Command {
	return []*cobra.Command{
		newAgentSchemaShowCmd(),
		newAgentAddCmd(),
		newAgentRemoveCmd(),
		newAgentSetCmd(),
		newAgentNewCmd(),
	}
}

func newAgentSchemaShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema [agent]",
		Short: "Print an agent's options schema as JSON (every agent's without an argument)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cat, err := clientFromContext(cmd.Context()).GetAgentSchemas()
			if err != nil {
				return fmt.Errorf("fetching agent schemas: %w", err)
			}
			if len(args) == 0 {
				data, _ := json.MarshalIndent(cat.Schemas, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return nil
			}
			s, ok := cat.ByName(args[0])
			if !ok {
				return fmt.Errorf("no agent %q", args[0])
			}
			fmt.Fprintln(cmd.OutOrStdout(), s.PrettyJSON())
			return nil
		},
	}
}

func newAgentAddCmd() *cobra.Command {
	var file, name string
	cmd := &cobra.Command{
		Use:   "add -f <schema.json>",
		Short: "Create or replace a custom agent from a JSON schema",
		Long: "Create or replace a custom agent from a JSON schema file.\n\n" +
			"The daemon accepts it only when config.toml has [ai] allow_custom_agents_api = true;\n" +
			"otherwise this prints the config.toml section to add by hand.",
		RunE: func(cmd *cobra.Command, args []string) error {
			schema, err := readSchemaFile(cmd.InOrStdin(), file)
			if err != nil {
				return err
			}
			if name != "" {
				schema.Name = name
			}
			c := clientFromContext(cmd.Context())
			cat, err := c.GetAgentSchemas()
			if err != nil {
				return fmt.Errorf("fetching agent schemas: %w", err)
			}
			if errs := schema.ValidateCustom(reservedAgentNames(cat)); len(errs) > 0 {
				return fmt.Errorf("invalid schema: %s", strings.Join(errs, "; "))
			}
			if err := c.PutAgentSchema(schema); err != nil {
				var httpErr *api.HTTPError
				if errors.As(err, &httpErr) && httpErr.Status == http.StatusForbidden {
					fmt.Fprintf(cmd.OutOrStdout(),
						"The daemon does not let the API define custom agents. Add this to config.toml:\n\n%s", schema.TOMLSnippet())
					return fmt.Errorf("custom agents API is disabled ([ai] allow_custom_agents_api)")
				}
				return fmt.Errorf("saving %s: %s", schema.Name, apiErrorText(err))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Saved custom agent %s.\n", schema.Name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "schema file (- for stdin)")
	cmd.Flags().StringVar(&name, "name", "", "agent name (defaults to the schema's name)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func readSchemaFile(stdin io.Reader, path string) (api.AgentSchema, error) {
	var r io.Reader = stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return api.AgentSchema{}, err
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, maxSchemaFile+1))
	if err != nil {
		return api.AgentSchema{}, err
	}
	if len(data) > maxSchemaFile {
		return api.AgentSchema{}, fmt.Errorf("schema is larger than %d bytes", maxSchemaFile)
	}
	var schema api.AgentSchema
	if err := json.Unmarshal(data, &schema); err != nil {
		return api.AgentSchema{}, fmt.Errorf("invalid schema JSON: %w", err)
	}
	return schema, nil
}

func reservedAgentNames(cat *api.AgentSchemaCatalog) map[string]bool {
	reserved := map[string]bool{"cursor": true}
	for _, s := range cat.Schemas {
		if s.Builtin {
			reserved[s.Name] = true
		}
	}
	return reserved
}

func apiErrorText(err error) string {
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Message()
	}
	return err.Error()
}

func newAgentRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <agent>",
		Short: "Delete a custom agent (needs [ai] allow_custom_agents_api = true)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := clientFromContext(cmd.Context()).DeleteAgentSchema(args[0]); err != nil {
				return fmt.Errorf("removing %s: %s", args[0], apiErrorText(err))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed custom agent %s.\n", args[0])
			return nil
		},
	}
}

func newAgentSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <agent> <key>=<value>...",
		Short: "Change an agent's settings; the keys come from its schema (`agents schema <agent>`)",
		Long: "Change an agent's settings. Each key is a field of the agent's schema; an empty\n" +
			"value unsets it. Dangerous toggles can only be turned off here.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			cat, err := c.GetAgentSchemas()
			if err != nil {
				return fmt.Errorf("fetching agent schemas: %w", err)
			}
			schema, ok := cat.ByName(args[0])
			if !ok {
				return fmt.Errorf("no agent %q", args[0])
			}
			values, err := parseAgentSettings(schema, args[1:])
			if err != nil {
				return err
			}
			if err := c.PatchConfig(api.AgentSettingsPatch(schema.Name, values)); err != nil {
				return fmt.Errorf("saving %s: %s", schema.Name, apiErrorText(err))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Updated %s.\n", schema.Name)
			return nil
		},
	}
}

// parseAgentSettings turns key=value arguments into typed values, the way the
// daemon stores "not set": "" for text and selects, 0 for numbers.
func parseAgentSettings(schema api.AgentSchema, pairs []string) (map[string]any, error) {
	values := map[string]any{}
	for _, pair := range pairs {
		key, raw, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("%q: use key=value", pair)
		}
		field, ok := schema.Field(key)
		if !ok {
			var keys []string
			for _, f := range schema.Fields {
				keys = append(keys, f.Key)
			}
			return nil, fmt.Errorf("%s has no setting %q (settings: %s)", schema.Name, key, strings.Join(keys, ", "))
		}
		v, err := field.ParseValue(raw)
		if err != nil {
			return nil, err
		}
		if v == nil {
			if field.Type == api.FieldNumber {
				v = 0
			} else {
				v = ""
			}
		}
		values[key] = v
	}
	return values, nil
}

func newAgentNewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "new",
		Short: "Create a custom agent with a guided wizard",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			cat, err := c.GetAgentSchemas()
			if err != nil {
				return fmt.Errorf("fetching agent schemas: %w", err)
			}
			return tui.RunAgentWizard(c, cat, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}
