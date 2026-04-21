import 'package:flutter/material.dart';

import '../../../core/models/activity.dart';

/// One row in the activity timeline.
class ActivityEntryTile extends StatelessWidget {
  final ActivityEntry entry;
  final VoidCallback? onTap;

  const ActivityEntryTile({super.key, required this.entry, this.onTap});

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final iconColor = entry.action == ActivityAction.error
        ? scheme.error
        : scheme.primary;

    return ListTile(
      leading: Icon(_iconFor(entry.action), color: iconColor),
      title: Text(
        '${entry.repo} · #${entry.itemNumber} · ${entry.itemTitle}',
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
      ),
      subtitle: Text(_subtitle(entry)),
      trailing: Text(
        _hhmmss(entry.timestamp),
        style: const TextStyle(fontFamily: 'monospace'),
      ),
      onTap: onTap,
    );
  }

  static IconData _iconFor(ActivityAction a) {
    switch (a) {
      case ActivityAction.review:    return Icons.rate_review;
      case ActivityAction.triage:    return Icons.label;
      case ActivityAction.implement: return Icons.build;
      case ActivityAction.promote:   return Icons.swap_horiz;
      case ActivityAction.error:     return Icons.error_outline;
      case ActivityAction.unknown:   return Icons.help_outline;
    }
  }

  static String _subtitle(ActivityEntry e) {
    switch (e.action) {
      case ActivityAction.review:
        final cli = e.details['cli_used'];
        final suffix = (cli is String && cli.isNotEmpty) ? ' by $cli' : '';
        return '${e.outcome} review$suffix';
      case ActivityAction.triage:
        final cat = e.details['category'];
        final catStr = (cat is String && cat.isNotEmpty) ? ' ($cat)' : '';
        return 'triaged${e.outcome.isEmpty ? '' : ': ${e.outcome}'}$catStr';
      case ActivityAction.implement:
        final n = e.details['pr_number'];
        if (n is num && n > 0) return 'opened PR #${n.toInt()}';
        return 'implement failed';
      case ActivityAction.promote:
        return 'promoted: ${e.outcome}';
      case ActivityAction.error:
        return e.outcome;
      case ActivityAction.unknown:
        return '';
    }
  }

  static String _hhmmss(DateTime t) =>
      '${t.hour.toString().padLeft(2, '0')}:${t.minute.toString().padLeft(2, '0')}:${t.second.toString().padLeft(2, '0')}';
}
