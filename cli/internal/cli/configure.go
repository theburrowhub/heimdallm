package cli

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newConfigureCmd() *cobra.Command {
	var auto bool

	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Save daemon connection settings to config file",
		Long: `Save host and token to ~/.config/heimdallm/cli.toml.

Use --auto to auto-discover the token from a local Docker container named 'heimdallm'.
Without --auto, you will be prompted for host and token interactively.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _ := loadCLIConfig()
			if cfg == nil {
				cfg = &cliConfig{}
			}

			if auto {
				return configureAuto(cfg)
			}
			return configureInteractive(cmd, cfg)
		},
	}

	cmd.Flags().BoolVar(&auto, "auto", false, "auto-discover token from local Docker container")
	return cmd
}

func configureAuto(cfg *cliConfig) error {
	if cfg.Host == "" {
		cfg.Host = "http://localhost:7842"
	}

	fmt.Println("Detecting Docker container 'heimdallm'...")
	token, err := discoverDockerToken()
	if err != nil {
		return fmt.Errorf("auto-discovery failed: %w\nIs the heimdallm container running? Try: docker ps | grep heimdallm", err)
	}

	cfg.Token = token
	fmt.Println("Token retrieved from container")

	if err := saveCLIConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("Saved to %s\n", configPath())
	return nil
}

func configureInteractive(cmd *cobra.Command, cfg *cliConfig) error {
	scanner := bufio.NewScanner(cmd.InOrStdin())

	defaultHost := cfg.Host
	if defaultHost == "" {
		defaultHost = "http://localhost:7842"
	}

	fmt.Printf("Host [%s]: ", defaultHost)
	if scanner.Scan() {
		if h := strings.TrimSpace(scanner.Text()); h != "" {
			cfg.Host = h
		} else {
			cfg.Host = defaultHost
		}
	}

	fmt.Print("Token: ")
	if scanner.Scan() {
		if t := strings.TrimSpace(scanner.Text()); t != "" {
			cfg.Token = t
		}
	}

	if err := saveCLIConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("Saved to %s\n", configPath())
	return nil
}
