package notify

import "log/slog"

type Notifier struct{}

func New() *Notifier { return &Notifier{} }

// Notify logs the notification. OS-level notifications are handled by the
// Flutter app via SSE events so the daemon never calls osascript directly
// (osascript associates notifications with Script Editor, not Heimdallm).
func (n *Notifier) Notify(title, message string) {
	slog.Info("notify", "title", title, "message", message)
}
