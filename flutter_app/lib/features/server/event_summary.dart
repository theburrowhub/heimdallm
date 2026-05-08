import 'package:flutter/material.dart';

/// One-line human summary for an SSE event payload, used by the Server
/// screen's Live Events tab. Pure function — no widget dependencies.
String summarize(String type, Map<String, dynamic> payload) {
  String repoRef() {
    final repo = payload['repo'] as String?;
    final number = payload['number'];
    if (repo != null && number != null) return '$repo#$number';
    if (repo != null) return repo;
    return '';
  }

  switch (type) {
    case 'pr_detected':
      return 'pr_detected  ${repoRef()}'.trimRight();
    case 'review_started':
      final agent = payload['agent'] as String?;
      final ref = repoRef();
      return agent != null && agent.isNotEmpty
          ? 'review_started  $ref ($agent)'
          : 'review_started  $ref';
    case 'review_completed':
      final dur = _durationLabel(payload['duration_ms']);
      return dur != null
          ? 'review_completed  ${repoRef()} in $dur'
          : 'review_completed  ${repoRef()}';
    case 'review_error':
      return 'review_error  ${repoRef()}';
    case 'review_skipped':
      final reason = payload['reason'] as String?;
      return reason != null
          ? 'review_skipped  ${repoRef()} ($reason)'
          : 'review_skipped  ${repoRef()}';
    case 'issue_detected':
      return 'issue_detected  ${repoRef()}';
    case 'issue_review_started':
      final agent = payload['agent'] as String?;
      return agent != null
          ? 'issue_review_started  ${repoRef()} ($agent)'
          : 'issue_review_started  ${repoRef()}';
    case 'issue_review_completed':
      final dur = _durationLabel(payload['duration_ms']);
      return dur != null
          ? 'issue_review_completed  ${repoRef()} in $dur'
          : 'issue_review_completed  ${repoRef()}';
    case 'issue_refinement_done':
      return 'issue_refinement_done  ${repoRef()}';
    case 'issue_implemented':
      return 'issue_implemented  ${repoRef()}';
    case 'issue_review_error':
      return 'issue_review_error  ${repoRef()}';
    case 'issue_promoted':
      final from = payload['from_stage'] as String?;
      final to = payload['to_stage'] as String?;
      if (from != null && to != null) {
        return 'issue_promoted  ${repoRef()}  $from → $to';
      }
      return 'issue_promoted  ${repoRef()}';
    case 'pr_state_changed':
    case 'issue_state_changed':
      final from = payload['old_state'] ?? payload['from'];
      final to = payload['new_state'] ?? payload['to'];
      if (from != null && to != null) {
        return '$type  ${repoRef()}  $from → $to';
      }
      return '$type  ${repoRef()}';
    case 'circuit_breaker_tripped':
      final reason = payload['reason'] as String?;
      return reason != null
          ? 'circuit_breaker_tripped  $reason'
          : 'circuit_breaker_tripped';
    case 'repo_discovered':
      final repo = payload['repo'] as String? ?? '';
      return 'repo_discovered  $repo'.trimRight();
    case 'polling_started':
      final kind = payload['kind'] as String? ?? '';
      final repos = (payload['repos'] as List?) ?? const [];
      return 'polling_started  $kind (${repos.length} repos)';
    case 'polling_completed':
      final kind = payload['kind'] as String? ?? '';
      final count = payload['count'] ?? 0;
      final ms = payload['duration_ms'] ?? 0;
      return 'polling_completed  $kind  $count items in ${ms}ms';
    default:
      final repo = repoRef();
      return repo.isNotEmpty ? '$type  $repo' : type;
  }
}

String? _durationLabel(dynamic ms) {
  if (ms is! num) return null;
  if (ms < 1000) return '${ms}ms';
  return '${(ms / 1000).toStringAsFixed(1)}s';
}

/// Glyph (icon + color) for an event type, used by the Live Events row
/// renderer.
({IconData icon, Color color}) glyphFor(String type) {
  switch (type) {
    case 'pr_detected':
    case 'issue_detected':
      return (icon: Icons.fiber_manual_record, color: const Color(0xFF6CA0FF));
    case 'review_started':
    case 'issue_review_started':
      return (icon: Icons.play_arrow, color: const Color(0xFFFFB347));
    case 'review_completed':
    case 'issue_review_completed':
    case 'issue_implemented':
    case 'issue_refinement_done':
      return (icon: Icons.check, color: const Color(0xFF6CCA6C));
    case 'review_error':
    case 'issue_review_error':
      return (icon: Icons.close, color: const Color(0xFFFF6B6B));
    case 'review_skipped':
      return (icon: Icons.remove, color: const Color(0xFF888888));
    case 'issue_promoted':
    case 'pr_state_changed':
    case 'issue_state_changed':
      return (icon: Icons.sync_alt, color: const Color(0xFFB070FF));
    case 'circuit_breaker_tripped':
      return (icon: Icons.warning_amber, color: const Color(0xFFFFB347));
    case 'repo_discovered':
      return (icon: Icons.add, color: const Color(0xFF6CA0FF));
    case 'polling_started':
      return (icon: Icons.more_horiz, color: const Color(0xFF888888));
    case 'polling_completed':
      return (icon: Icons.circle_outlined, color: const Color(0xFF888888));
    default:
      return (icon: Icons.label_outline, color: const Color(0xFFD4D4D4));
  }
}
