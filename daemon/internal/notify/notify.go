package notify

import (
	"fmt"
	"log/slog"
	"os/exec"
)

type Notifier struct{}

func New() *Notifier {
	return &Notifier{}
}

// Notify displays a macOS notification using osascript.
// Silently logs errors — notifications are best-effort.
func (n *Notifier) Notify(title, message string) {
	script := fmt.Sprintf(`display notification %q with title %q`, message, title)
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		slog.Debug("notify: osascript failed", "err", err)
	}
}
