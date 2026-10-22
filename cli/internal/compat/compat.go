// Package compat declares which daemon API revision this CLI needs.
package compat

// MinDaemonAPI is the oldest daemon API revision (the api_version the daemon
// reports on /health) this CLI works against. Raise it in the same change that
// makes the CLI depend on a newer endpoint or payload, together with
// "min_daemon_api.cli" in the repository's compat.json (a test keeps them
// equal). The release pipeline publishes that file, and `heimdallm-cli update`
// warns before installing a CLI that is ahead of the connected daemon.
const MinDaemonAPI = 1

// BaselineDaemonAPI is the revision assumed for a daemon that reports no
// api_version: daemons built before the field existed served revision 1.
const BaselineDaemonAPI = 1

// EffectiveDaemonAPI maps a reported api_version to the revision it stands
// for, treating an absent (zero) value as the baseline.
func EffectiveDaemonAPI(reported int) int {
	if reported <= 0 {
		return BaselineDaemonAPI
	}
	return reported
}
