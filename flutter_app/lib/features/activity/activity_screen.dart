import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/models/activity.dart';
import 'activity_providers.dart';
import 'widgets/activity_entry_tile.dart';
import 'widgets/activity_filter_chips.dart';

/// The "Activity log" tab content. Shows a date picker bar, filter chips,
/// and a timeline of activity entries grouped by hour.
///
/// No Scaffold/AppBar — this is rendered inside the dashboard's TabBarView,
/// which supplies the shared AppBar.
class ActivityScreen extends ConsumerWidget {
  const ActivityScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final q = ref.watch(activityQueryProvider);
    final async = ref.watch(activityEntriesProvider);

    final orgs = async.valueOrNull?.entries.map((e) => e.org).toSet().toList()
        ?? const <String>[];
    final repos = async.valueOrNull?.entries.map((e) => e.repo).toSet().toList()
        ?? const <String>[];

    return Column(
      children: [
        _DatePickerBar(query: q),
        Padding(
          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
          child: ActivityFilterChips(
            availableOrgs: orgs,
            availableRepos: repos,
          ),
        ),
        const Divider(height: 1),
        Expanded(
          child: async.when(
            data: (page) => _Timeline(page: page),
            loading: () => const Center(child: CircularProgressIndicator()),
            error: (err, _) => Center(
              child: Padding(
                padding: const EdgeInsets.all(24),
                child: Text('Error: $err'),
              ),
            ),
          ),
        ),
      ],
    );
  }
}

class _DatePickerBar extends ConsumerWidget {
  final ActivityQuery query;
  const _DatePickerBar({required this.query});

  static bool _isSameDay(DateTime? a, DateTime b) =>
      a != null && a.year == b.year && a.month == b.month && a.day == b.day;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final notifier   = ref.read(activityQueryProvider.notifier);
    final today      = DateTime.now();
    final yesterday  = today.subtract(const Duration(days: 1));
    final isToday    = _isSameDay(query.date, today);
    final isYesterday = _isSameDay(query.date, yesterday);

    return SingleChildScrollView(
      scrollDirection: Axis.horizontal,
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
      child: Row(children: [
        ChoiceChip(
          label: const Text('Today'),
          selected: isToday,
          onSelected: (_) => notifier.setDate(today),
        ),
        const SizedBox(width: 8),
        ChoiceChip(
          label: const Text('Yesterday'),
          selected: isYesterday,
          onSelected: (_) => notifier.setDate(yesterday),
        ),
        const SizedBox(width: 8),
        ActionChip(
          label: const Text('Pick day'),
          onPressed: () async {
            final picked = await showDatePicker(
              context: context,
              initialDate: query.date ?? today,
              firstDate: today.subtract(const Duration(days: 365)),
              lastDate: today,
            );
            if (picked != null) notifier.setDate(picked);
          },
        ),
        const SizedBox(width: 8),
        ActionChip(
          label: const Text('Pick range'),
          onPressed: () async {
            final picked = await showDateRangePicker(
              context: context,
              firstDate: today.subtract(const Duration(days: 365)),
              lastDate: today,
            );
            if (picked != null) notifier.setRange(picked.start, picked.end);
          },
        ),
      ]),
    );
  }
}

class _Timeline extends StatelessWidget {
  final ActivityPage page;
  const _Timeline({required this.page});

  @override
  Widget build(BuildContext context) {
    if (page.entries.isEmpty) {
      return const Center(child: Text('No activity for this period.'));
    }

    final items = <Widget>[];
    if (page.truncated) {
      items.add(Container(
        width: double.infinity,
        padding: const EdgeInsets.all(12),
        color: Theme.of(context).colorScheme.surfaceVariant,
        child: Text(
          'Showing ${page.entries.length} most recent entries. Narrow filters to see more.',
        ),
      ));
    }

    int? currentHour;
    for (final e in page.entries) {
      if (e.timestamp.hour != currentHour) {
        currentHour = e.timestamp.hour;
        items.add(Padding(
          padding: const EdgeInsets.fromLTRB(16, 12, 16, 4),
          child: Text(
            '${currentHour.toString().padLeft(2, '0')}:00',
            style: Theme.of(context).textTheme.titleSmall,
          ),
        ));
      }
      items.add(ActivityEntryTile(
        entry: e,
        onTap: () {
          // Tap → detail navigation is deferred: the activity_log row knows
          // repo + number but not the PR/issue store ID the /prs/:id and
          // /issues/:id routes expect. Follow-up spec (AI report generation)
          // will add a by-number lookup endpoint.
          ScaffoldMessenger.of(context).showSnackBar(
            SnackBar(
              content: Text('${e.repo} #${e.itemNumber}'),
              duration: const Duration(seconds: 2),
            ),
          );
        },
      ));
    }

    return ListView(children: items);
  }
}
