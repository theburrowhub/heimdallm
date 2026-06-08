import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../core/models/activity.dart';
import '../activity_providers.dart';

/// Three chips (Organization / Repository / Action) with multi-select popups,
/// plus a Clear-filters button when any filter is active.
class ActivityFilterChips extends ConsumerWidget {
  final List<String> availableOrgs;
  final List<String> availableRepos;
  final List<String> availableOutcomes;
  final bool optionsLimited;

  const ActivityFilterChips({
    super.key,
    required this.availableOrgs,
    required this.availableRepos,
    this.availableOutcomes = const [],
    this.optionsLimited = false,
  });

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final q = ref.watch(activityQueryProvider);
    final anyActive =
        q.orgs.isNotEmpty ||
        q.repos.isNotEmpty ||
        q.itemTypes.isNotEmpty ||
        q.actions.isNotEmpty ||
        q.outcomes.isNotEmpty;

    return Wrap(
      spacing: 8,
      runSpacing: 4,
      crossAxisAlignment: WrapCrossAlignment.center,
      children: [
        _quickChip(
          label: 'PRs',
          selected: q.itemTypes.contains('pr'),
          onSelected: (v) => ref
              .read(activityQueryProvider.notifier)
              .setQuickFilter(itemType: 'pr', enabled: v),
        ),
        _quickChip(
          label: 'Issues',
          selected: q.itemTypes.contains('issue'),
          onSelected: (v) => ref
              .read(activityQueryProvider.notifier)
              .setQuickFilter(itemType: 'issue', enabled: v),
        ),
        _quickChip(
          label: 'Skipped',
          selected: q.actions.contains(ActivityAction.reviewSkipped),
          onSelected: (v) => ref
              .read(activityQueryProvider.notifier)
              .setQuickFilter(action: ActivityAction.reviewSkipped, enabled: v),
        ),
        _quickChip(
          label: 'Errors',
          selected: q.actions.contains(ActivityAction.error),
          onSelected: (v) => ref
              .read(activityQueryProvider.notifier)
              .setQuickFilter(action: ActivityAction.error, enabled: v),
        ),
        _quickChip(
          label: 'Draft skips',
          selected:
              q.actions.contains(ActivityAction.reviewSkipped) &&
              q.outcomes.contains('draft'),
          onSelected: (v) => ref
              .read(activityQueryProvider.notifier)
              .setQuickFilter(
                action: ActivityAction.reviewSkipped,
                outcome: 'draft',
                enabled: v,
              ),
        ),
        _chip(
          context,
          label: 'Organization',
          count: q.orgs.length,
          onTap: () => _pickStrings(
            context,
            options: availableOrgs,
            optionsLimited: optionsLimited,
            select: (q) => q.orgs,
            toggle: (v) =>
                ref.read(activityQueryProvider.notifier).toggleOrg(v),
          ),
        ),
        _chip(
          context,
          label: 'Repository',
          count: q.repos.length,
          onTap: () => _pickStrings(
            context,
            options: availableRepos,
            optionsLimited: optionsLimited,
            select: (q) => q.repos,
            toggle: (v) =>
                ref.read(activityQueryProvider.notifier).toggleRepo(v),
          ),
        ),
        _chip(
          context,
          label: 'Type',
          count: q.itemTypes.length,
          onTap: () => _pickStrings(
            context,
            options: const ['pr', 'issue'],
            select: (q) => q.itemTypes,
            toggle: (v) =>
                ref.read(activityQueryProvider.notifier).toggleItemType(v),
            labelFor: _itemTypeLabel,
          ),
        ),
        _chip(
          context,
          label: 'Action',
          count: q.actions.length,
          onTap: () => _pickActions(
            context,
            toggle: (a) =>
                ref.read(activityQueryProvider.notifier).toggleAction(a),
          ),
        ),
        if (availableOutcomes.isNotEmpty)
          _chip(
            context,
            label: 'Outcome',
            count: q.outcomes.length,
            onTap: () => _pickStrings(
              context,
              options: availableOutcomes,
              optionsLimited: optionsLimited,
              select: (q) => q.outcomes,
              toggle: (v) =>
                  ref.read(activityQueryProvider.notifier).toggleOutcome(v),
            ),
          ),
        if (anyActive)
          TextButton(
            onPressed: ref.read(activityQueryProvider.notifier).clearFilters,
            child: const Text('Clear filters'),
          ),
      ],
    );
  }

  Widget _chip(
    BuildContext context, {
    required String label,
    required int count,
    required VoidCallback onTap,
  }) {
    return ActionChip(
      label: Text(count == 0 ? label : '$label · $count'),
      onPressed: onTap,
    );
  }

  Widget _quickChip({
    required String label,
    required bool selected,
    required ValueChanged<bool> onSelected,
  }) {
    return FilterChip(
      label: Text(label),
      selected: selected,
      onSelected: onSelected,
    );
  }

  Future<void> _pickStrings(
    BuildContext context, {
    required List<String> options,
    bool optionsLimited = false,
    required Set<String> Function(ActivityQuery q) select,
    required void Function(String) toggle,
    String Function(String)? labelFor,
  }) async {
    await showModalBottomSheet<void>(
      context: context,
      builder: (_) => Consumer(
        builder: (ctx, ref, _) {
          final selected = select(ref.watch(activityQueryProvider));
          if (options.isEmpty) {
            return const SafeArea(
              child: SizedBox(
                height: 160,
                child: Center(child: Text('No options available')),
              ),
            );
          }
          return ListView(
            children: [
              if (optionsLimited)
                Padding(
                  padding: const EdgeInsets.fromLTRB(16, 12, 16, 4),
                  child: Text(
                    'Options limited to visible activity',
                    style: Theme.of(ctx).textTheme.bodySmall?.copyWith(
                      color: Theme.of(ctx).colorScheme.onSurfaceVariant,
                    ),
                  ),
                ),
              ...options.map(
                (o) => CheckboxListTile(
                  value: selected.contains(o),
                  title: Text(labelFor?.call(o) ?? o),
                  onChanged: (_) => toggle(o),
                ),
              ),
            ],
          );
        },
      ),
    );
  }

  Future<void> _pickActions(
    BuildContext context, {
    required void Function(ActivityAction) toggle,
  }) async {
    const options = [
      ActivityAction.review,
      ActivityAction.reviewSkipped,
      ActivityAction.triage,
      ActivityAction.refinement,
      ActivityAction.implement,
      ActivityAction.promote,
      ActivityAction.error,
    ];
    await showModalBottomSheet<void>(
      context: context,
      builder: (_) => Consumer(
        builder: (ctx, ref, _) {
          final selected = ref.watch(activityQueryProvider).actions;
          return ListView(
            children: options
                .map(
                  (a) => CheckboxListTile(
                    value: selected.contains(a),
                    title: Text(_actionLabel(a)),
                    onChanged: (_) => toggle(a),
                  ),
                )
                .toList(),
          );
        },
      ),
    );
  }

  static String _itemTypeLabel(String itemType) => switch (itemType) {
    'pr' => 'Pull requests',
    'issue' => 'Issues',
    _ => itemType,
  };

  static String _actionLabel(ActivityAction action) => switch (action) {
    ActivityAction.review => 'Reviews',
    ActivityAction.reviewSkipped => 'Skipped reviews',
    ActivityAction.triage => 'Triage',
    ActivityAction.refinement => 'Refinement',
    ActivityAction.implement => 'Implementation',
    ActivityAction.promote => 'Promotion',
    ActivityAction.error => 'Errors',
    ActivityAction.unknown => 'Unknown',
  };
}
