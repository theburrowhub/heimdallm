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
    final optionsAsync = ref.watch(activityOptionsProvider);

    final orgs = optionsAsync.valueOrNull?.entries
            .map((e) => e.org).toSet().toList()
        ?? const <String>[];
    final repos = optionsAsync.valueOrNull?.entries
            .map((e) => e.repo).toSet().toList()
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
            error: (err, _) => _ErrorView(error: err),
          ),
        ),
      ],
    );
  }
}

class _ErrorView extends StatelessWidget {
  final Object error;
  const _ErrorView({required this.error});

  @override
  Widget build(BuildContext context) {
    if (error is ActivityDisabledException) {
      return const Center(
        child: Padding(
          padding: EdgeInsets.all(24),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              Icon(Icons.toggle_off_outlined, size: 48, color: Colors.grey),
              SizedBox(height: 12),
              Text('Activity log is disabled',
                  style: TextStyle(fontWeight: FontWeight.w600)),
              SizedBox(height: 4),
              Text(
                'Enable activity_log in the daemon config to start recording activity.',
                style: TextStyle(color: Colors.grey),
                textAlign: TextAlign.center,
              ),
            ],
          ),
        ),
      );
    }
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(24),
        child: Text('Could not load activity: $error'),
      ),
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
              initialDateRange: (query.from != null && query.to != null)
                  ? DateTimeRange(start: query.from!, end: query.to!)
                  : null,
            );
            if (picked != null) notifier.setRange(picked.start, picked.end);
          },
        ),
      ]),
    );
  }
}

/// An item in the timeline: either a date/hour header or an entry tile.
/// Pre-flattening the sequence lets us render with ListView.builder so tiles
/// are built lazily and rebuilds don't allocate the full widget tree.
sealed class _TimelineItem {
  const _TimelineItem();
}

class _TruncationBanner extends _TimelineItem {
  final int shown;
  const _TruncationBanner(this.shown);
}

class _DateHeader extends _TimelineItem {
  final DateTime day;
  const _DateHeader(this.day);
}

class _HourHeader extends _TimelineItem {
  final int hour;
  const _HourHeader(this.hour);
}

class _EntryItem extends _TimelineItem {
  final ActivityEntry entry;
  const _EntryItem(this.entry);
}

class _Timeline extends StatelessWidget {
  final ActivityPage page;
  const _Timeline({required this.page});

  static bool _sameDay(DateTime a, DateTime b) =>
      a.year == b.year && a.month == b.month && a.day == b.day;

  List<_TimelineItem> _buildItems() {
    final items = <_TimelineItem>[];
    if (page.truncated) {
      items.add(_TruncationBanner(page.entries.length));
    }

    DateTime? currentDay;
    int? currentHour;
    for (final e in page.entries) {
      final ts = e.timestamp;
      if (currentDay == null || !_sameDay(currentDay, ts)) {
        items.add(_DateHeader(DateTime(ts.year, ts.month, ts.day)));
        currentDay = ts;
        currentHour = null;
      }
      if (ts.hour != currentHour) {
        items.add(_HourHeader(ts.hour));
        currentHour = ts.hour;
      }
      items.add(_EntryItem(e));
    }
    return items;
  }

  @override
  Widget build(BuildContext context) {
    if (page.entries.isEmpty) {
      return const Center(child: Text('No activity for this period.'));
    }

    final items = _buildItems();
    return ListView.builder(
      itemCount: items.length,
      itemBuilder: (context, i) => _renderItem(context, items[i]),
    );
  }

  Widget _renderItem(BuildContext context, _TimelineItem item) {
    switch (item) {
      case _TruncationBanner(:final shown):
        return Container(
          width: double.infinity,
          padding: const EdgeInsets.all(12),
          color: Theme.of(context).colorScheme.surfaceContainerHighest,
          child: Text(
            'Showing $shown most recent entries. Narrow filters to see more.',
          ),
        );
      case _DateHeader(:final day):
        return Padding(
          padding: const EdgeInsets.fromLTRB(16, 16, 16, 4),
          child: Text(
            _formatDay(day),
            style: Theme.of(context).textTheme.titleMedium?.copyWith(
                  fontWeight: FontWeight.bold,
                ),
          ),
        );
      case _HourHeader(:final hour):
        return Padding(
          padding: const EdgeInsets.fromLTRB(16, 8, 16, 4),
          child: Text(
            '${hour.toString().padLeft(2, '0')}:00',
            style: Theme.of(context).textTheme.titleSmall,
          ),
        );
      case _EntryItem(:final entry):
        return ActivityEntryTile(
          entry: entry,
          onTap: () {
            // Tap → detail navigation is deferred: the activity_log row knows
            // repo + number but not the PR/issue store ID the /prs/:id and
            // /issues/:id routes expect. Follow-up spec (AI report generation)
            // will add a by-number lookup endpoint.
            final messenger = ScaffoldMessenger.of(context);
            messenger.hideCurrentSnackBar();
            messenger.showSnackBar(
              SnackBar(
                content: Text('${entry.repo} #${entry.itemNumber}'),
                duration: const Duration(seconds: 2),
              ),
            );
          },
        );
    }
  }

  static String _formatDay(DateTime d) {
    const months = [
      'Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun',
      'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec',
    ];
    return '${months[d.month - 1]} ${d.day}, ${d.year}';
  }
}
