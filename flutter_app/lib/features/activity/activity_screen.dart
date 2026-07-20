import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:url_launcher/url_launcher.dart';

import '../../core/api/api_client.dart';
import '../../core/models/activity.dart';
import '../dashboard/dashboard_providers.dart' show apiClientProvider;
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
    ref.watch(activityLiveRefreshProvider);
    final async = ref.watch(activityEntriesProvider);
    final optionsAsync = ref.watch(activityOptionsProvider);

    final optionEntries =
        optionsAsync.value?.entries ??
        async.value?.entries ??
        const <ActivityEntry>[];
    final usingFallbackOptions =
        optionsAsync.value == null && async.value != null;
    final orgs = _sortedDistinct(optionEntries.map((e) => e.org));
    final repos = _sortedDistinct(optionEntries.map((e) => e.repo));
    final outcomes = _sortedDistinct(optionEntries.map((e) => e.outcome));

    return Column(
      children: [
        _DatePickerBar(query: q),
        Padding(
          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
          child: ActivityFilterChips(
            availableOrgs: orgs,
            availableRepos: repos,
            availableOutcomes: outcomes,
            optionsLimited: usingFallbackOptions,
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

List<String> _sortedDistinct(Iterable<String> values) {
  final list = values.where((v) => v.isNotEmpty).toSet().toList();
  list.sort();
  return list;
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
              Text(
                'Activity log is disabled',
                style: TextStyle(fontWeight: FontWeight.w600),
              ),
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
    final notifier = ref.read(activityQueryProvider.notifier);
    final live = ref.watch(activityLiveUpdatesProvider);
    final today = DateTime.now();
    final yesterday = today.subtract(const Duration(days: 1));
    final isToday = _isSameDay(query.date, today);
    final isYesterday = _isSameDay(query.date, yesterday);

    return SingleChildScrollView(
      scrollDirection: Axis.horizontal,
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
      child: Row(
        children: [
          ElevatedButton.icon(
            icon: const Icon(Icons.add, size: 18),
            label: const Text('ADD'),
            onPressed: () => _showAddPRDialog(context),
          ),
          const SizedBox(width: 12),
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
          const SizedBox(width: 8),
          FilterChip(
            label: const Text('Live'),
            selected: live,
            onSelected: (v) =>
                ref.read(activityLiveUpdatesProvider.notifier).set(v),
          ),
          const SizedBox(width: 4),
          IconButton(
            tooltip: 'Refresh activity',
            icon: const Icon(Icons.refresh),
            onPressed: () {
              ref.invalidate(activityEntriesProvider);
              ref.invalidate(activityOptionsProvider);
            },
          ),
        ],
      ),
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
            style: Theme.of(
              context,
            ).textTheme.titleMedium?.copyWith(fontWeight: FontWeight.bold),
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
          onTap: () => _showActivityDetail(context, entry),
        );
    }
  }

  static String _formatDay(DateTime d) {
    const months = [
      'Jan',
      'Feb',
      'Mar',
      'Apr',
      'May',
      'Jun',
      'Jul',
      'Aug',
      'Sep',
      'Oct',
      'Nov',
      'Dec',
    ];
    return '${months[d.month - 1]} ${d.day}, ${d.year}';
  }
}

void _showActivityDetail(BuildContext context, ActivityEntry entry) {
  showModalBottomSheet<void>(
    context: context,
    isScrollControlled: true,
    constraints: const BoxConstraints(maxWidth: 760),
    builder: (_) => _ActivityDetailSheet(entry: entry),
  );
}

class _ActivityDetailSheet extends StatelessWidget {
  final ActivityEntry entry;
  const _ActivityDetailSheet({required this.entry});

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final details = const JsonEncoder.withIndent('  ').convert(entry.details);
    final githubUrl = _githubUrl(entry);

    return SafeArea(
      child: SingleChildScrollView(
        padding: const EdgeInsets.fromLTRB(20, 18, 20, 24),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                ActivityActionBadge(action: entry.action),
                const SizedBox(width: 8),
                ActivityItemTypeBadge(itemType: entry.itemType),
                const Spacer(),
                IconButton(
                  tooltip: 'Close',
                  icon: const Icon(Icons.close),
                  onPressed: () => Navigator.of(context).pop(),
                ),
              ],
            ),
            const SizedBox(height: 14),
            Text(
              activityEntryTitle(entry),
              style: Theme.of(
                context,
              ).textTheme.titleMedium?.copyWith(fontWeight: FontWeight.w700),
            ),
            const SizedBox(height: 6),
            Text(
              activityOutcomeText(entry),
              style: Theme.of(
                context,
              ).textTheme.bodyMedium?.copyWith(color: scheme.onSurfaceVariant),
            ),
            const SizedBox(height: 18),
            _DetailRow(label: 'Repository', value: entry.repo),
            _DetailRow(label: 'Number', value: '#${entry.itemNumber}'),
            _DetailRow(label: 'Title', value: entry.itemTitle),
            _DetailRow(label: 'Action', value: entry.action.wireName),
            _DetailRow(label: 'Outcome', value: entry.outcome),
            _DetailRow(label: 'Time', value: _formatTimestamp(entry.timestamp)),
            const SizedBox(height: 14),
            OutlinedButton.icon(
              icon: const Icon(Icons.open_in_new, size: 16),
              label: const Text('Open in GitHub'),
              onPressed: () async {
                final opened = await launchUrl(githubUrl);
                if (!context.mounted || opened) return;
                ScaffoldMessenger.of(context).showSnackBar(
                  SnackBar(content: Text('Could not open $githubUrl')),
                );
              },
            ),
            if (entry.details.isNotEmpty) ...[
              const SizedBox(height: 20),
              Text(
                'Details',
                style: Theme.of(
                  context,
                ).textTheme.titleSmall?.copyWith(fontWeight: FontWeight.w700),
              ),
              const SizedBox(height: 8),
              Container(
                width: double.infinity,
                padding: const EdgeInsets.all(12),
                decoration: BoxDecoration(
                  color: scheme.surfaceContainerHighest.withValues(alpha: 0.55),
                  borderRadius: BorderRadius.circular(6),
                  border: Border.all(color: scheme.outlineVariant),
                ),
                child: SelectableText(
                  details,
                  style: const TextStyle(fontFamily: 'monospace', fontSize: 12),
                ),
              ),
            ],
          ],
        ),
      ),
    );
  }

  static Uri _githubUrl(ActivityEntry entry) {
    final kind = entry.itemType == 'issue' ? 'issues' : 'pull';
    return Uri.parse(
      'https://github.com/${entry.repo}/$kind/${entry.itemNumber}',
    );
  }

  static String _formatTimestamp(DateTime t) {
    final date =
        '${t.year.toString().padLeft(4, '0')}-${t.month.toString().padLeft(2, '0')}-${t.day.toString().padLeft(2, '0')}';
    return '$date ${activityTimeLabel(t)}';
  }
}

Future<void> _showAddPRDialog(BuildContext context) {
  return showDialog<void>(
    context: context,
    builder: (_) => const _AddPRDialog(),
  );
}

/// Dialog for the Activity view "ADD" action: paste a GitHub PR URL. On submit
/// the daemon adds the PR's repository to the monitored list and reviews the
/// PR immediately.
class _AddPRDialog extends ConsumerStatefulWidget {
  const _AddPRDialog();

  @override
  ConsumerState<_AddPRDialog> createState() => _AddPRDialogState();
}

class _AddPRDialogState extends ConsumerState<_AddPRDialog> {
  final _controller = TextEditingController();
  bool _submitting = false;
  String? _error;

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  static bool _looksLikePRUrl(String s) {
    final t = s.trim();
    return t.contains('github.com/') && t.contains('/pull/');
  }

  Future<void> _submit() async {
    final url = _controller.text.trim();
    if (!_looksLikePRUrl(url)) {
      setState(
        () => _error =
            'Enter a GitHub PR link, e.g. https://github.com/owner/repo/pull/123',
      );
      return;
    }
    setState(() {
      _submitting = true;
      _error = null;
    });
    try {
      await ref.read(apiClientProvider).addPRByUrl(url);
      if (!mounted) return;
      Navigator.of(context).pop();
      ref.invalidate(activityEntriesProvider);
      ref.invalidate(activityOptionsProvider);
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(
          content: Text('PR added — repository monitored and review started.'),
        ),
      );
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _submitting = false;
        _error = e is ApiException ? e.message : e.toString();
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      title: const Text('Add a pull request'),
      content: SizedBox(
        width: 460,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text(
              'Paste a GitHub PR link. Its repository is added to the '
              'monitored list and the PR is reviewed right away.',
            ),
            const SizedBox(height: 14),
            TextField(
              controller: _controller,
              autofocus: true,
              enabled: !_submitting,
              keyboardType: TextInputType.url,
              decoration: const InputDecoration(
                labelText: 'GitHub PR URL',
                hintText: 'https://github.com/owner/repo/pull/123',
                border: OutlineInputBorder(),
              ),
              onSubmitted: (_) {
                if (!_submitting) _submit();
              },
            ),
            if (_error != null) ...[
              const SizedBox(height: 10),
              Text(
                _error!,
                style: TextStyle(color: Theme.of(context).colorScheme.error),
              ),
            ],
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: _submitting ? null : () => Navigator.of(context).pop(),
          child: const Text('Cancel'),
        ),
        FilledButton(
          onPressed: _submitting ? null : _submit,
          child: _submitting
              ? const SizedBox(
                  width: 16,
                  height: 16,
                  child: CircularProgressIndicator(strokeWidth: 2),
                )
              : const Text('Add & review'),
        ),
      ],
    );
  }
}

class _DetailRow extends StatelessWidget {
  final String label;
  final String value;
  const _DetailRow({required this.label, required this.value});

  @override
  Widget build(BuildContext context) {
    if (value.isEmpty) return const SizedBox.shrink();
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          SizedBox(
            width: 96,
            child: Text(
              label,
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                color: Theme.of(context).colorScheme.onSurfaceVariant,
                fontWeight: FontWeight.w600,
              ),
            ),
          ),
          Expanded(child: SelectableText(value)),
        ],
      ),
    );
  }
}
