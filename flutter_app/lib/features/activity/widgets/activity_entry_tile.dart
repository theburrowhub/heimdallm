import 'package:flutter/material.dart';

import '../../../core/models/activity.dart';

/// One row in the activity timeline.
class ActivityEntryTile extends StatelessWidget {
  final ActivityEntry entry;
  final VoidCallback? onTap;

  const ActivityEntryTile({super.key, required this.entry, this.onTap});

  @override
  Widget build(BuildContext context) {
    final divider = Theme.of(context).dividerColor.withValues(alpha: 0.45);

    return Material(
      color: Colors.transparent,
      child: InkWell(
        onTap: onTap,
        child: Container(
          decoration: BoxDecoration(
            border: Border(bottom: BorderSide(color: divider)),
          ),
          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
          child: LayoutBuilder(
            builder: (context, constraints) {
              if (constraints.maxWidth >= 760) {
                return _WideEntryRow(entry: entry);
              }
              return _CompactEntryRow(entry: entry);
            },
          ),
        ),
      ),
    );
  }
}

class _WideEntryRow extends StatelessWidget {
  final ActivityEntry entry;
  const _WideEntryRow({required this.entry});

  @override
  Widget build(BuildContext context) {
    return Row(
      children: [
        SizedBox(
          width: 78,
          child: Text(
            activityTimeLabel(entry.timestamp),
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              fontFamily: 'monospace',
              fontWeight: FontWeight.w600,
            ),
          ),
        ),
        SizedBox(width: 128, child: ActivityActionBadge(action: entry.action)),
        const SizedBox(width: 8),
        SizedBox(
          width: 64,
          child: ActivityItemTypeBadge(itemType: entry.itemType),
        ),
        const SizedBox(width: 12),
        Expanded(child: _EntryText(entry: entry)),
      ],
    );
  }
}

class _CompactEntryRow extends StatelessWidget {
  final ActivityEntry entry;
  const _CompactEntryRow({required this.entry});

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            ActivityActionBadge(action: entry.action),
            const SizedBox(width: 8),
            ActivityItemTypeBadge(itemType: entry.itemType),
            const Spacer(),
            Text(
              activityTimeLabel(entry.timestamp),
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                fontFamily: 'monospace',
                fontWeight: FontWeight.w600,
              ),
            ),
          ],
        ),
        const SizedBox(height: 6),
        _EntryText(entry: entry),
      ],
    );
  }
}

class _EntryText extends StatelessWidget {
  final ActivityEntry entry;
  const _EntryText({required this.entry});

  @override
  Widget build(BuildContext context) {
    final titleStyle = Theme.of(
      context,
    ).textTheme.bodyMedium?.copyWith(fontWeight: FontWeight.w600);
    final subtitleStyle = Theme.of(context).textTheme.bodySmall?.copyWith(
      color: Theme.of(context).colorScheme.onSurfaceVariant,
    );

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          activityEntryTitle(entry),
          maxLines: 1,
          overflow: TextOverflow.ellipsis,
          style: titleStyle,
        ),
        const SizedBox(height: 2),
        Text(
          activityOutcomeText(entry),
          maxLines: 1,
          overflow: TextOverflow.ellipsis,
          style: subtitleStyle,
        ),
      ],
    );
  }
}

class ActivityActionBadge extends StatelessWidget {
  final ActivityAction action;
  const ActivityActionBadge({super.key, required this.action});

  @override
  Widget build(BuildContext context) {
    final color = activityActionColor(Theme.of(context).colorScheme, action);
    return Container(
      height: 28,
      constraints: const BoxConstraints(maxWidth: 120),
      padding: const EdgeInsets.symmetric(horizontal: 8),
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.12),
        borderRadius: BorderRadius.circular(6),
        border: Border.all(color: color.withValues(alpha: 0.28)),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(activityIconFor(action), size: 15, color: color),
          const SizedBox(width: 6),
          Flexible(
            child: Text(
              activityActionLabel(action),
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(
                color: color,
                fontWeight: FontWeight.w700,
                fontSize: 12,
              ),
            ),
          ),
        ],
      ),
    );
  }
}

class ActivityItemTypeBadge extends StatelessWidget {
  final String itemType;
  const ActivityItemTypeBadge({super.key, required this.itemType});

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final isIssue = itemType == 'issue';
    final color = isIssue ? scheme.tertiary : scheme.primary;
    final label = isIssue ? 'Issue' : 'PR';
    return Container(
      height: 28,
      alignment: Alignment.center,
      padding: const EdgeInsets.symmetric(horizontal: 8),
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.10),
        borderRadius: BorderRadius.circular(6),
        border: Border.all(color: color.withValues(alpha: 0.22)),
      ),
      child: Text(
        label,
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
        style: TextStyle(
          color: color,
          fontWeight: FontWeight.w700,
          fontSize: 12,
        ),
      ),
    );
  }
}

IconData activityIconFor(ActivityAction action) => switch (action) {
  ActivityAction.review => Icons.rate_review,
  ActivityAction.reviewSkipped => Icons.visibility_off_outlined,
  ActivityAction.triage => Icons.label,
  ActivityAction.refinement => Icons.fact_check,
  ActivityAction.implement => Icons.build,
  ActivityAction.promote => Icons.swap_horiz,
  ActivityAction.error => Icons.error_outline,
  ActivityAction.unknown => Icons.help_outline,
};

String activityActionLabel(ActivityAction action) => switch (action) {
  ActivityAction.review => 'Review',
  ActivityAction.reviewSkipped => 'Skipped',
  ActivityAction.triage => 'Triage',
  ActivityAction.refinement => 'Refinement',
  ActivityAction.implement => 'Implement',
  ActivityAction.promote => 'Promote',
  ActivityAction.error => 'Error',
  ActivityAction.unknown => 'Unknown',
};

Color activityActionColor(ColorScheme scheme, ActivityAction action) =>
    switch (action) {
      ActivityAction.error => scheme.error,
      ActivityAction.reviewSkipped => scheme.outline,
      ActivityAction.triage => scheme.tertiary,
      ActivityAction.refinement => scheme.tertiary,
      ActivityAction.implement => scheme.secondary,
      ActivityAction.promote => scheme.primary,
      ActivityAction.review => scheme.primary,
      ActivityAction.unknown => scheme.outline,
    };

String activityEntryTitle(ActivityEntry entry) {
  final title = entry.itemTitle.trim();
  final suffix = title.isEmpty ? '' : ' · $title';
  return '${entry.repo} · #${entry.itemNumber}$suffix';
}

String activityOutcomeText(ActivityEntry entry) => switch (entry.action) {
  ActivityAction.review => _reviewOutcome(entry),
  ActivityAction.reviewSkipped => _skippedOutcome(entry),
  ActivityAction.triage => _triageOutcome(entry),
  ActivityAction.refinement => _refinementOutcome(entry),
  ActivityAction.implement => _implementOutcome(entry),
  ActivityAction.promote => 'Promoted: ${entry.outcome}',
  ActivityAction.error => entry.outcome.isEmpty ? 'Error' : entry.outcome,
  ActivityAction.unknown =>
    entry.outcome.isEmpty
        ? 'Unknown activity action'
        : 'Unknown: ${entry.outcome}',
};

String activityTimeLabel(DateTime t) =>
    '${t.hour.toString().padLeft(2, '0')}:${t.minute.toString().padLeft(2, '0')}:${t.second.toString().padLeft(2, '0')}';

String _reviewOutcome(ActivityEntry entry) {
  final cli = entry.details['cli_used'];
  final suffix = (cli is String && cli.isNotEmpty) ? ' by $cli' : '';
  final severity = entry.outcome.isEmpty ? 'completed' : entry.outcome;
  return '$severity review$suffix';
}

String _skippedOutcome(ActivityEntry entry) {
  final reason = _detailString(entry, 'reason') ?? entry.outcome;
  final label = _skipReasonLabel(reason);
  return label.isEmpty ? 'Skipped review' : 'Skipped because $label';
}

String _triageOutcome(ActivityEntry entry) {
  final cat = entry.details['category'];
  final catStr = (cat is String && cat.isNotEmpty) ? ' ($cat)' : '';
  return 'Triaged${entry.outcome.isEmpty ? '' : ': ${entry.outcome}'}$catStr';
}

String _refinementOutcome(ActivityEntry entry) {
  final posted = entry.details['post_ok'];
  final base = posted == false
      ? 'Refinement stored locally'
      : 'Refinement completed';
  return entry.details['truncated'] == true ? '$base (truncated)' : base;
}

String _implementOutcome(ActivityEntry entry) {
  final n = entry.details['pr_number'];
  if (n is num && n > 0) return 'Opened PR #${n.toInt()}';
  return 'Implementation failed';
}

String _skipReasonLabel(String reason) => switch (reason) {
  'draft' => 'PR is draft',
  'not_open' => 'PR is not open',
  'self_authored' => 'PR was authored by the bot',
  'sha_unchanged' => 'HEAD SHA is unchanged',
  'legacy_backfill' => 'legacy review was backfilled',
  _ => reason.replaceAll('_', ' '),
};

String? _detailString(ActivityEntry entry, String key) {
  final value = entry.details[key];
  if (value is String && value.isNotEmpty) return value;
  return null;
}
