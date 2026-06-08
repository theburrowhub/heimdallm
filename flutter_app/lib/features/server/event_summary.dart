import 'package:flutter/material.dart';

/// EventStatus drives the colour + icon family of a row. Kept narrow on
/// purpose — adding a new bucket should be deliberate.
enum EventStatus {
  /// Work that just started and may still be running (review_started,
  /// issue_review_started, polling_started, …).
  started,

  /// Terminal-success: review_completed, issue_implemented, polling_completed.
  succeeded,

  /// Terminal-failure: review_error, issue_review_error.
  failed,

  /// Intentionally not run: review_skipped, dismissed, dedup hit.
  skipped,

  /// Operator/observation-only: pr_detected, repo_discovered, state changes.
  info,

  /// Soft-warning: circuit_breaker_tripped, stage promotion.
  warning,
}

/// FormattedEvent is the structured view of an SSE event for the Server
/// section's Live Events tab. Pure data class — the rendering widget
/// decides how to lay out the fields.
///
/// Why structured instead of a single string: #453 — the previous
/// summarize() collapsed everything into a one-liner, so the GUI either
/// dumped raw JSON on expand or showed a wall of underscored type names
/// like `polling_started`. With the fields split, the row can show a
/// human-readable label (`Polling started`) up top and the target /
/// details on a second line, and the JSON expand stays as a debugging
/// fallback rather than the primary view.
@immutable
class FormattedEvent {
  /// Human-readable title: `Review started`, `Issue triaged`, etc.
  /// Capitalised, no underscores, no event-id leakage.
  final String label;

  /// Subject of the event when known: `org/repo#42` for a PR, `org/repo`
  /// for repo-scoped events, empty for global events (polling cycle).
  final String target;

  /// Extra context: agent name, duration, stage transition, error reason.
  /// Rendered as separator-joined or chip-style spans by the row widget.
  final List<String> details;

  /// Status bucket — drives icon + colour. See [EventStatus].
  final EventStatus status;

  /// Glyph for the row. Kept separate from status so type-specific icons
  /// (sync_alt for promotion, warning_amber for breaker) survive while
  /// the colour palette stays tied to status.
  final IconData icon;

  /// Accent colour applied to the icon + status indicator.
  final Color color;

  const FormattedEvent({
    required this.label,
    required this.target,
    required this.details,
    required this.status,
    required this.icon,
    required this.color,
  });
}

/// Palette tied to [EventStatus]. Hex values match the prior glyphFor()
/// choices so the visual continuity carries over from the old one-line
/// renderer. A switch expression — rather than a map + `!` — keeps the
/// match exhaustive at compile time so adding a new [EventStatus] won't
/// silently regress to a runtime null when the enum drifts ahead of the
/// palette.
Color _color(EventStatus s) => switch (s) {
      EventStatus.started => const Color(0xFFFFB347), // orange
      EventStatus.succeeded => const Color(0xFF6CCA6C), // green
      EventStatus.failed => const Color(0xFFFF6B6B), // red
      EventStatus.skipped => const Color(0xFF888888), // gray
      EventStatus.info => const Color(0xFF6CA0FF), // blue
      EventStatus.warning => const Color(0xFFB070FF), // purple
    };

/// Build a [FormattedEvent] from the raw SSE wire payload. Each branch
/// owns its label + status + icon + which payload keys feed the target
/// and details fields. The default branch keeps unknown event types
/// renderable — degrade gracefully instead of dropping the row.
FormattedEvent format(String type, Map<String, dynamic> payload) {
  switch (type) {
    case 'pr_detected':
      return FormattedEvent(
        label: 'PR detected',
        target: _repoRef(payload),
        details: const [],
        status: EventStatus.info,
        icon: Icons.fiber_manual_record,
        color: _color(EventStatus.info),
      );
    case 'review_started':
      return FormattedEvent(
        label: 'Review started',
        target: _repoRef(payload),
        details: _agentDetails(payload),
        status: EventStatus.started,
        icon: Icons.play_arrow,
        color: _color(EventStatus.started),
      );
    case 'review_completed':
      return FormattedEvent(
        label: 'Review completed',
        target: _repoRef(payload),
        details: _durationDetails(payload),
        status: EventStatus.succeeded,
        icon: Icons.check,
        color: _color(EventStatus.succeeded),
      );
    case 'review_error':
      return FormattedEvent(
        label: 'Review failed',
        target: _repoRef(payload),
        details: _errorDetails(payload),
        status: EventStatus.failed,
        icon: Icons.close,
        color: _color(EventStatus.failed),
      );
    case 'review_skipped':
      return FormattedEvent(
        label: 'Review skipped',
        target: _repoRef(payload),
        details: _reasonDetails(payload),
        status: EventStatus.skipped,
        icon: Icons.remove,
        color: _color(EventStatus.skipped),
      );
    case 'issue_detected':
      return FormattedEvent(
        label: 'Issue detected',
        target: _repoRef(payload),
        details: const [],
        status: EventStatus.info,
        icon: Icons.fiber_manual_record,
        color: _color(EventStatus.info),
      );
    case 'issue_review_started':
      return FormattedEvent(
        label: 'Triage started',
        target: _repoRef(payload),
        details: _agentDetails(payload),
        status: EventStatus.started,
        icon: Icons.play_arrow,
        color: _color(EventStatus.started),
      );
    case 'issue_review_completed':
      return FormattedEvent(
        label: 'Triage completed',
        target: _repoRef(payload),
        details: _durationDetails(payload),
        status: EventStatus.succeeded,
        icon: Icons.check,
        color: _color(EventStatus.succeeded),
      );
    case 'issue_refinement_done':
      return FormattedEvent(
        label: 'Refinement completed',
        target: _repoRef(payload),
        details: _durationDetails(payload),
        status: EventStatus.succeeded,
        icon: Icons.check,
        color: _color(EventStatus.succeeded),
      );
    case 'issue_implemented':
      return FormattedEvent(
        label: 'Issue implemented',
        target: _repoRef(payload),
        details: _durationDetails(payload),
        status: EventStatus.succeeded,
        icon: Icons.check,
        color: _color(EventStatus.succeeded),
      );
    case 'issue_review_error':
      return FormattedEvent(
        label: 'Triage failed',
        target: _repoRef(payload),
        details: _errorDetails(payload),
        status: EventStatus.failed,
        icon: Icons.close,
        color: _color(EventStatus.failed),
      );
    case 'issue_promoted':
      return FormattedEvent(
        label: 'Stage promoted',
        target: _repoRef(payload),
        details: _stageDetails(payload),
        status: EventStatus.warning,
        icon: Icons.sync_alt,
        color: _color(EventStatus.warning),
      );
    case 'pr_state_changed':
      return FormattedEvent(
        label: 'PR state changed',
        target: _repoRef(payload),
        details: _stateChangeDetails(payload),
        status: EventStatus.info,
        icon: Icons.sync_alt,
        color: _color(EventStatus.info),
      );
    case 'issue_state_changed':
      return FormattedEvent(
        label: 'Issue state changed',
        target: _repoRef(payload),
        details: _stateChangeDetails(payload),
        status: EventStatus.info,
        icon: Icons.sync_alt,
        color: _color(EventStatus.info),
      );
    case 'circuit_breaker_tripped':
      return FormattedEvent(
        label: 'Circuit breaker tripped',
        target: _repoRef(payload),
        details: _reasonDetails(payload),
        status: EventStatus.warning,
        icon: Icons.warning_amber,
        color: _color(EventStatus.warning),
      );
    case 'repo_discovered':
      return FormattedEvent(
        label: 'Repo discovered',
        target: (payload['repo'] as String?) ?? '',
        details: const [],
        status: EventStatus.info,
        icon: Icons.add,
        color: _color(EventStatus.info),
      );
    case 'polling_started':
      return FormattedEvent(
        label: 'Polling started',
        target: '',
        details: _pollingStartDetails(payload),
        status: EventStatus.started,
        icon: Icons.more_horiz,
        color: _color(EventStatus.started),
      );
    case 'polling_completed':
      return FormattedEvent(
        label: 'Polling completed',
        target: '',
        details: _pollingCompletedDetails(payload),
        status: EventStatus.succeeded,
        icon: Icons.circle_outlined,
        color: _color(EventStatus.succeeded),
      );
    default:
      return FormattedEvent(
        label: type,
        target: _repoRef(payload),
        details: const [],
        status: EventStatus.info,
        icon: Icons.label_outline,
        color: const Color(0xFFD4D4D4),
      );
  }
}

/// `org/repo#42` for PR/issue-scoped events, `org/repo` for repo-only.
String _repoRef(Map<String, dynamic> payload) {
  final repo = payload['repo'] as String?;
  final number = payload['number'];
  if (repo != null && number != null) return '$repo#$number';
  if (repo != null) return repo;
  return '';
}

List<String> _agentDetails(Map<String, dynamic> payload) {
  final agent = payload['agent'] as String?;
  if (agent == null || agent.isEmpty) return const [];
  return [agent];
}

List<String> _durationDetails(Map<String, dynamic> payload) {
  final label = _durationLabel(payload['duration_ms']);
  return label == null ? const [] : [label];
}

List<String> _errorDetails(Map<String, dynamic> payload) {
  final err = payload['error'] as String?;
  if (err == null || err.isEmpty) return const [];
  // Errors can be long — truncate at the row level so the chip stays
  // readable. The full string remains accessible via the row's expand.
  if (err.length > 80) return ['${err.substring(0, 77)}…'];
  return [err];
}

List<String> _reasonDetails(Map<String, dynamic> payload) {
  final reason = payload['reason'] as String?;
  if (reason == null || reason.isEmpty) return const [];
  return [reason];
}

List<String> _stageDetails(Map<String, dynamic> payload) {
  final from = payload['from_stage'] as String?;
  final to = payload['to_stage'] as String?;
  final trigger = payload['trigger'] as String?;
  final out = <String>[];
  if (from != null && to != null) out.add('$from → $to');
  if (trigger != null && trigger.isNotEmpty) out.add(trigger);
  return out;
}

List<String> _stateChangeDetails(Map<String, dynamic> payload) {
  final from = payload['old_state'] ?? payload['from'];
  final to = payload['new_state'] ?? payload['to'];
  if (from != null && to != null) return ['$from → $to'];
  return const [];
}

List<String> _pollingStartDetails(Map<String, dynamic> payload) {
  final kind = payload['kind'] as String? ?? '';
  final repos = (payload['repos'] as List?) ?? const [];
  final out = <String>[];
  if (kind.isNotEmpty) out.add(kind);
  out.add('${repos.length} repos');
  return out;
}

List<String> _pollingCompletedDetails(Map<String, dynamic> payload) {
  final kind = payload['kind'] as String? ?? '';
  final count = payload['count'] ?? 0;
  final dur = _durationLabel(payload['duration_ms']);
  final out = <String>[];
  if (kind.isNotEmpty) out.add(kind);
  out.add('$count items');
  if (dur != null) out.add(dur);
  return out;
}

String? _durationLabel(dynamic ms) {
  if (ms is! num) return null;
  if (ms < 1000) return '${ms}ms';
  return '${(ms / 1000).toStringAsFixed(1)}s';
}
