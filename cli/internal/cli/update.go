package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
	"github.com/theburrowhub/heimdallm/cli/internal/compat"
	"github.com/theburrowhub/heimdallm/cli/internal/update"
)

const (
	defaultCheckInterval = 24 * time.Hour
	minCheckInterval     = time.Minute
	hintCheckTimeout     = 2 * time.Second
	updateStateFile      = "update-check.json"
)

// autoUpdateConfig is the [auto_update] table of cli.toml.
type autoUpdateConfig struct {
	Enabled       *bool  `toml:"enabled,omitempty"`
	CheckInterval string `toml:"check_interval,omitempty"`
	Channel       string `toml:"channel,omitempty"`
}

func (c *cliConfig) autoUpdateEnabled() bool {
	if c == nil || c.AutoUpdate == nil || c.AutoUpdate.Enabled == nil {
		return true
	}
	return *c.AutoUpdate.Enabled
}

func (c *cliConfig) updateInterval() time.Duration {
	if c == nil || c.AutoUpdate == nil || c.AutoUpdate.CheckInterval == "" {
		return defaultCheckInterval
	}
	d, err := time.ParseDuration(c.AutoUpdate.CheckInterval)
	if err != nil || d < minCheckInterval {
		return defaultCheckInterval
	}
	return d
}

func (c *cliConfig) updateChannel() string {
	if c == nil || c.AutoUpdate == nil || c.AutoUpdate.Channel == "" {
		return update.ChannelStable
	}
	return c.AutoUpdate.Channel
}

// Seams for tests.
var (
	newUpdater     = update.New
	executablePath = func() (string, error) {
		p, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(p)
	}
)

func isDevVersion(version string) bool {
	_, err := update.Compare(version, "0.0.0")
	return err != nil
}

func configuredChannel(flag string) (string, error) {
	cfg, _ := loadCLIConfig()
	channel := flag
	if channel == "" {
		channel = cfg.updateChannel()
	}
	if channel != update.ChannelStable && channel != update.ChannelLatest {
		return "", fmt.Errorf("unknown channel %q (want %q or %q)", channel, update.ChannelStable, update.ChannelLatest)
	}
	return channel, nil
}

func newVersionCmd(version string) *cobra.Command {
	var check bool
	var channel string
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version, optionally checking for a newer release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "heimdallm-cli version %s\n", version)
			if !check {
				return nil
			}
			ch, err := configuredChannel(channel)
			if err != nil {
				return err
			}
			up := newUpdater()
			rel, err := up.Latest(cmd.Context(), ch)
			if err != nil {
				return fmt.Errorf("checking for updates: %w", err)
			}
			if err := reportCheck(out, version, rel); err != nil {
				return err
			}
			warnIfAheadOfDaemon(cmd, up, version, rel)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "check for a newer release without installing it")
	cmd.Flags().StringVar(&channel, "channel", "", "release channel: stable or latest (default: auto_update.channel, else stable)")
	return cmd
}

func reportCheck(out io.Writer, current string, rel *update.Release) error {
	newer, err := isNewer(current, rel)
	if err != nil {
		fmt.Fprintf(out, "Latest release: %s\n", rel.Tag)
		return nil
	}
	if !newer {
		fmt.Fprintf(out, "Up to date (latest release: %s).\n", rel.Tag)
		return nil
	}
	fmt.Fprintf(out, "Update available: %s -> %s\nRun `heimdallm-cli update` to install it.\n", current, rel.Tag)
	return nil
}

// daemonHealthTimeout bounds the /health probe behind the compatibility
// warning: it is advisory, so an unreachable daemon must not stall an update.
const daemonHealthTimeout = 3 * time.Second

// warnIfAheadOfDaemon prints a warning, never an error, when the release about
// to be installed needs a newer daemon API than the connected daemon serves.
// Releases without compatibility data, an unreachable daemon or a release that
// is not newer print nothing: the warning only speaks on evidence.
func warnIfAheadOfDaemon(cmd *cobra.Command, up *update.Updater, current string, rel *update.Release) {
	if newer, err := isNewer(current, rel); err != nil || !newer {
		return
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), daemonHealthTimeout)
	defer cancel()
	c, err := up.Compat(ctx, rel)
	if err != nil {
		return
	}
	need, ok := c.MinDaemonAPI["cli"]
	if !ok {
		return
	}
	client := daemonProbeClient(cmd)
	h, err := client.GetHealth()
	if err != nil {
		return
	}
	have := compat.EffectiveDaemonAPI(h.APIVersion)
	if have >= need {
		return
	}
	daemon := "the daemon"
	if v := h.DisplayVersion(); v != "" {
		daemon = "daemon " + v
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"Warning: heimdallm-cli %s needs daemon API %d or newer, but %s at %s serves API %d.\n"+
			"The CLI can still be installed; update the daemon too (desktop app, package or Docker image).\n",
		rel.Version(), need, daemon, client.Host, have)
}

// daemonProbeClient points at the daemon the other commands would talk to
// (--host, then HEIMDALLM_HOST, then cli.toml). update and version skip the
// root's client resolution so they work with no daemon configured; this
// resolves the host lazily, without exiting on a bad config. /health needs no
// credential, so the probe never sends the token anywhere.
func daemonProbeClient(cmd *cobra.Command) *api.Client {
	host, _ := cmd.Flags().GetString("host")
	instance, _ := cmd.Flags().GetString("instance")
	if host == "" {
		host = os.Getenv("HEIMDALLM_HOST")
	}
	if host == "" {
		if cfg, err := loadCLIConfig(); err == nil {
			if h, _, err := cfg.resolve(instance); err == nil {
				host = h
			}
		}
	}
	c := api.New(host, "")
	c.HTTPClient = &http.Client{Timeout: daemonHealthTimeout}
	return c
}

func isNewer(current string, rel *update.Release) (bool, error) {
	c, err := update.Compare(rel.Tag, current)
	if err != nil {
		return false, err
	}
	return c > 0, nil
}

func newUpdateCmd(version string) *cobra.Command {
	var check bool
	var channel string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update heimdallm-cli to the latest release",
		Long: "Download the latest heimdallm-cli release for this OS/arch, verify it against the\n" +
			"release's cli-checksums.txt and replace the running binary. The previous binary is\n" +
			"kept next to it as heimdallm-cli.bak and restored if the new one fails to start.\n" +
			"This only updates the CLI; the daemon and the desktop app update on their own.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			ch, err := configuredChannel(channel)
			if err != nil {
				return err
			}
			up := newUpdater()
			rel, err := up.Latest(cmd.Context(), ch)
			if err != nil {
				return fmt.Errorf("checking for updates: %w", err)
			}
			if check {
				if err := reportCheck(out, version, rel); err != nil {
					return err
				}
				warnIfAheadOfDaemon(cmd, up, version, rel)
				return nil
			}
			if isDevVersion(version) {
				return fmt.Errorf("this is a development build (%q); install a release to enable self-update", version)
			}
			newer, err := isNewer(version, rel)
			if err != nil {
				return err
			}
			if !newer {
				fmt.Fprintf(out, "Already up to date (%s).\n", version)
				return nil
			}
			exe, err := executablePath()
			if err != nil {
				return fmt.Errorf("locating the running binary: %w", err)
			}
			if managedByPackageManager(exe) {
				return fmt.Errorf("%s is managed by Homebrew; run `brew upgrade theburrowhub/tap/heimdallm-cli` instead", exe)
			}
			if bundledWithDesktopApp(exe) {
				return fmt.Errorf("%s ships inside the Heimdallm desktop installer; update the desktop app instead (it replaces the bundled CLI)", exe)
			}
			warnIfAheadOfDaemon(cmd, up, version, rel)
			fmt.Fprintf(out, "Updating %s -> %s ...\n", version, rel.Tag)
			if err := up.Apply(cmd.Context(), rel, exe); err != nil {
				return err
			}
			fmt.Fprintf(out, "Updated to %s. Previous version kept at %s.\n", rel.Tag, update.BackupPath(exe))
			if notes := update.ChangelogSummary(rel.Body, 20); notes != "" {
				fmt.Fprintf(out, "\nWhat's new in %s:\n%s\n", rel.Tag, notes)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only check for a newer release")
	cmd.Flags().StringVar(&channel, "channel", "", "release channel: stable or latest (default: auto_update.channel, else stable)")
	return cmd
}

func managedByPackageManager(exe string) bool {
	p := filepath.ToSlash(exe)
	return strings.Contains(p, "/Cellar/") || strings.Contains(p, "/Homebrew/")
}

// bundledWithDesktopApp reports whether exe is the copy of the CLI shipped by
// the desktop installers (Heimdallm.app/Contents/MacOS on macOS, /opt/heimdallm
// for the .deb/.rpm). Rewriting it would break the app bundle's code seal or a
// package-owned file, so the desktop updater owns it.
func bundledWithDesktopApp(exe string) bool {
	p := filepath.ToSlash(exe)
	return strings.Contains(p, ".app/Contents/MacOS/") || strings.HasPrefix(p, "/opt/heimdallm/")
}

type updateState struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest,omitempty"`
}

func loadUpdateState() updateState {
	var st updateState
	if data, err := os.ReadFile(filepath.Join(configDir(), updateStateFile)); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func saveUpdateState(st updateState) {
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(configDir(), updateStateFile), data, 0o600)
}

// hintSkipped lists commands where an update notice would be noise or would
// corrupt the output: the update commands themselves, the full-screen TUI,
// the streaming follow and shell completion.
var hintSkipped = map[string]bool{
	"update": true, "version": true, "dashboard": true, "follow": true,
	"completion": true, "help": true, "__complete": true,
}

func stderrIsTerminal() bool {
	st, err := os.Stderr.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// maybeHintUpdate prints a one-line notice on stderr when a newer release
// exists. It checks the network at most once per auto_update.check_interval
// and never applies anything.
func maybeHintUpdate(cmd *cobra.Command, version string) {
	maybeHint(cmd, version, stderrIsTerminal())
}

func maybeHint(cmd *cobra.Command, version string, interactive bool) {
	if isDevVersion(version) || hintSkipped[cmd.Name()] || !interactive {
		return
	}
	cfg, _ := loadCLIConfig()
	if !cfg.autoUpdateEnabled() {
		return
	}
	hintUpdate(cmd.ErrOrStderr(), cmd.Context(), cfg, version, time.Now())
}

func hintUpdate(w io.Writer, ctx context.Context, cfg *cliConfig, version string, now time.Time) {
	st := loadUpdateState()
	if st.CheckedAt.IsZero() || now.Sub(st.CheckedAt) >= cfg.updateInterval() || st.CheckedAt.After(now) {
		ctx, cancel := context.WithTimeout(ctx, hintCheckTimeout)
		defer cancel()
		next := updateState{CheckedAt: now}
		if rel, err := newUpdater().Latest(ctx, cfg.updateChannel()); err == nil {
			next.Latest = rel.Tag
		}
		saveUpdateState(next)
		st = next
	}
	if st.Latest == "" {
		return
	}
	if c, err := update.Compare(st.Latest, version); err == nil && c > 0 {
		fmt.Fprintf(w, "\nheimdallm-cli %s is available (current %s). Run `heimdallm-cli update`.\n", st.Latest, version)
	}
}
