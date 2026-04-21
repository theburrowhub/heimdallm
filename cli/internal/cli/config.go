package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Show the daemon's running configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			cfg, err := c.GetConfig()
			if err != nil {
				return fmt.Errorf("fetching config: %w", err)
			}

			b, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				return fmt.Errorf("formatting config: %w", err)
			}
			fmt.Println(string(b))
			return nil
		},
	}
}
