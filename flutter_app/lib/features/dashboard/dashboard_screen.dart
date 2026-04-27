import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:shared_preferences/shared_preferences.dart';
import '../../core/models/pr.dart';
import '../../core/models/tracked_issue.dart';
import '../../core/platform/platform_services_provider.dart';
import '../../shared/widgets/severity_badge.dart';
import '../../shared/widgets/state_badge.dart';
import '../../shared/widgets/toast.dart';
import '../../shared/widgets/type_badge.dart';
import '../activity/activity_screen.dart';
import '../activity/activity_providers.dart';
import '../agents/agents_screen.dart';
import '../circuit_breaker/circuit_breaker_banner.dart';
import '../cli_agents/cli_agents_screen.dart';
import '../config/config_providers.dart';
import '../issues/issues_providers.dart';
import '../repositories/repos_screen.dart';
import '../stats/stats_screen.dart';
import 'activity_filter_bar.dart';
import 'activity_filters.dart';
import 'dashboard_providers.dart';

class DashboardScreen extends ConsumerWidget {
  const DashboardScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final cbMessage = ref.watch(circuitBreakerProvider);
    final daemonRunning = ref.watch(daemonHealthProvider).valueOrNull ?? false;
    final daemonStarting = ref.watch(daemonStartingProvider);
    return DefaultTabController(
      length: 6,
      child: Scaffold(
        appBar: AppBar(
          title: const Text('Heimdallm'),
          actions: [
            IconButton(
              icon: daemonStarting
                  ? const SizedBox(
                      width: 20,
                      height: 20,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    )
                  : Icon(
                      daemonRunning
                          ? Icons.power_settings_new
                          : Icons.play_arrow,
                    ),
              tooltip: daemonRunning ? 'Stop Server' : 'Start Server',
              onPressed: daemonStarting
                  ? null
                  : daemonRunning
                  ? () => _confirmShutdown(context, ref)
                  : () => _startDaemon(context, ref),
            ),
            IconButton(
              icon: const Icon(Icons.article_outlined),
              tooltip: 'Daemon logs',
              onPressed: () => context.push('/logs'),
            ),
            IconButton(
              icon: const Icon(Icons.settings),
              onPressed: () => context.push('/config'),
            ),
            IconButton(
              icon: const Icon(Icons.refresh),
              onPressed: () {
                ref.invalidate(prsProvider);
                ref.invalidate(issuesProvider);
                ref.invalidate(statsProvider);
                ref.invalidate(activityEntriesProvider);
                ref.invalidate(activityOptionsProvider);
              },
            ),
          ],
          bottom: const TabBar(
            tabs: [
              Tab(icon: Icon(Icons.dashboard), text: 'Activity'),
              Tab(icon: Icon(Icons.timeline), text: 'Activity log'),
              Tab(icon: Icon(Icons.folder_outlined), text: 'Repositories'),
              Tab(icon: Icon(Icons.auto_awesome), text: 'Prompts'),
              Tab(icon: Icon(Icons.smart_toy), text: 'Agents'),
              Tab(icon: Icon(Icons.bar_chart), text: 'Stats'),
            ],
          ),
        ),
        body: Column(
          children: [
            if (cbMessage != null)
              CircuitBreakerBanner(
                message: cbMessage,
                onDismiss: () =>
                    ref.read(circuitBreakerProvider.notifier).state = null,
              ),
            const Expanded(
              child: TabBarView(
                children: [
                  _ActivityTab(),
                  ActivityScreen(),
                  ReposScreen(),
                  AgentsScreen(),
                  CLIAgentsScreen(),
                  StatsScreen(),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}

const _daemonStartHealthMaxAttempts = 80;
const _daemonStartHealthInterval = Duration(milliseconds: 100);

Future<void> _confirmShutdown(BuildContext context, WidgetRef ref) async {
  final confirmed = await showDialog<bool>(
    context: context,
    builder: (context) => AlertDialog(
      title: const Text('Stop Server?'),
      content: const Text('Active reviews may be interrupted.'),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(false),
          child: const Text('Cancel'),
        ),
        FilledButton(
          onPressed: () => Navigator.of(context).pop(true),
          child: const Text('Stop Server'),
        ),
      ],
    ),
  );
  if (confirmed != true || !context.mounted) return;

  try {
    await ref.read(apiClientProvider).shutdownDaemon();
    if (!context.mounted) return;
    showToast(context, 'Shutdown requested');
    ref.invalidate(sseStreamProvider);
    ref.invalidate(daemonHealthProvider);
    await _refreshWhenDaemonStops(context, ref);
  } catch (e) {
    if (context.mounted) showToast(context, 'Error: $e', isError: true);
  }
}

Future<void> _startDaemon(BuildContext context, WidgetRef ref) async {
  if (ref.read(daemonStartingProvider)) return;

  final platform = ref.read(platformServicesProvider);
  final binaryPath = platform.defaultDaemonBinaryPath();
  if (binaryPath == null || binaryPath.isEmpty) {
    showToast(context, 'Daemon binary not found', isError: true);
    return;
  }

  ref.read(daemonStartingProvider.notifier).state = true;
  try {
    await platform.spawnDaemon(binaryPath);
    final api = ref.read(apiClientProvider);
    var healthy = false;
    for (var i = 0; i < _daemonStartHealthMaxAttempts; i++) {
      await Future<void>.delayed(_daemonStartHealthInterval);
      healthy = await api.checkHealth();
      if (healthy) break;
    }
    _invalidateDashboardData(ref);
    if (!context.mounted) return;
    if (healthy) {
      showToast(context, 'Server started');
    } else {
      showToast(
        context,
        'Heimdallm could not start. Check the app installation.',
        isError: true,
      );
    }
  } catch (e) {
    if (context.mounted) showToast(context, 'Error: $e', isError: true);
  } finally {
    ref.read(daemonStartingProvider.notifier).state = false;
  }
}

Future<void> _refreshWhenDaemonStops(
  BuildContext context,
  WidgetRef ref,
) async {
  final api = ref.read(apiClientProvider);
  const delays = [
    Duration(milliseconds: 200),
    Duration(milliseconds: 300),
    Duration(milliseconds: 500),
    Duration(milliseconds: 800),
    Duration(milliseconds: 1200),
    Duration(seconds: 2),
  ];

  for (final delay in delays) {
    await Future<void>.delayed(delay);
    if (!context.mounted) return;
    final healthy = await api.checkHealth();
    if (!context.mounted) return;
    ref.invalidate(daemonHealthProvider);
    if (!healthy) break;
  }

  if (!context.mounted) return;
  _invalidateDashboardData(ref);
}

void _invalidateDashboardData(WidgetRef ref) {
  ref.invalidate(sseStreamProvider);
  ref.invalidate(daemonHealthProvider);
  ref.invalidate(prsProvider);
  ref.invalidate(issuesProvider);
  ref.invalidate(statsProvider);
  ref.invalidate(activityEntriesProvider);
  ref.invalidate(activityOptionsProvider);
}

// ── Reviews tab ──────────────────────────────────────────────────────────────

// SortMode is defined in activity_filters.dart (shared with activity_filter_bar)

const _sortPrefKey = 'activity_sort_mode';

final reviewsSortProvider = NotifierProvider<SortNotifier, SortMode>(
  SortNotifier.new,
);

class SortNotifier extends Notifier<SortMode> {
  @override
  SortMode build() {
    _loadAsync();
    return SortMode.priority;
  }

  void _loadAsync() async {
    try {
      final prefs = await SharedPreferences.getInstance();
      final value = prefs.getString(_sortPrefKey);
      if (value == 'newest') {
        state = SortMode.newest;
      }
    } catch (e) {
      debugPrint('SortNotifier: failed to load preference: $e');
    }
  }

  void set(SortMode mode) {
    state = mode;
    SharedPreferences.getInstance()
        .then((prefs) {
          prefs.setString(_sortPrefKey, mode.name);
        })
        .catchError((e) {
          debugPrint('SortNotifier: failed to save preference: $e');
        });
  }
}

// ── Unified activity item ────────────────────────────────────────────────────

sealed class _ActivityItem {
  const _ActivityItem();
  factory _ActivityItem.pr(PR pr) = _PRItem;
  factory _ActivityItem.issue(TrackedIssue issue) = _IssueItem;
}

class _PRItem extends _ActivityItem {
  final PR pr;
  const _PRItem(this.pr);
}

class _IssueItem extends _ActivityItem {
  final TrackedIssue issue;
  const _IssueItem(this.issue);
}

String _itemType(_ActivityItem item) => switch (item) {
  _PRItem() => 'pr',
  _IssueItem(:final issue) =>
    (issue.latestReview != null && issue.latestReview!.actionTaken == 'develop')
        ? 'dev'
        : 'it',
};

String _itemRepo(_ActivityItem item) => switch (item) {
  _PRItem(:final pr) => pr.repo,
  _IssueItem(:final issue) => issue.repo,
};

String _itemTitle(_ActivityItem item) => switch (item) {
  _PRItem(:final pr) => pr.title,
  _IssueItem(:final issue) => issue.title,
};

int _itemNumber(_ActivityItem item) => switch (item) {
  _PRItem(:final pr) => pr.number,
  _IssueItem(:final issue) => issue.number,
};

String _itemAuthor(_ActivityItem item) => switch (item) {
  _PRItem(:final pr) => pr.author,
  _IssueItem(:final issue) => issue.author,
};

DateTime _itemDate(_ActivityItem item) => switch (item) {
  _PRItem(:final pr) => pr.updatedAt,
  _IssueItem(:final issue) => issue.latestReview?.createdAt ?? issue.fetchedAt,
};

int _itemPriorityKey(_ActivityItem item) => switch (item) {
  _PRItem(:final pr) =>
    pr.latestReview == null
        ? 0
        : switch (pr.latestReview!.severity.toLowerCase()) {
            'high' => 1,
            'medium' => 2,
            _ => 3,
          },
  _IssueItem(:final issue) =>
    issue.latestReview == null
        ? 0
        : switch (issue.latestReview!.severity.toLowerCase()) {
            'critical' => 0,
            'high' => 1,
            'medium' => 2,
            _ => 3,
          },
};

String _itemState(_ActivityItem item) => switch (item) {
  _PRItem(:final pr) => pr.state,
  _IssueItem(:final issue) => issue.state,
};

bool _matchesFilters(_ActivityItem item, ActivityFilters filters) {
  // Type filter
  if (filters.types.isNotEmpty) {
    final type = _itemType(item);
    if (!filters.types.contains(type)) return false;
  }
  // Org filter
  if (filters.orgs.isNotEmpty) {
    final repo = _itemRepo(item);
    final org = repo.contains('/') ? repo.split('/').first : repo;
    if (!filters.orgs.contains(org)) return false;
  }
  // Repo filter
  if (filters.repos.isNotEmpty) {
    if (!filters.repos.contains(_itemRepo(item))) return false;
  }
  // State filter
  if (filters.states.isNotEmpty) {
    if (!filters.states.contains(_itemState(item))) return false;
  }
  // Search
  if (filters.search.isNotEmpty) {
    final q = filters.search.toLowerCase();
    final title = _itemTitle(item).toLowerCase();
    final repo = _itemRepo(item).toLowerCase();
    final number = _itemNumber(item).toString();
    final author = _itemAuthor(item).toLowerCase();
    if (!title.contains(q) &&
        !repo.contains(q) &&
        !number.contains(q) &&
        !author.contains(q)) {
      return false;
    }
  }
  return true;
}

void _sortItems(List<_ActivityItem> items, SortMode mode) {
  switch (mode) {
    case SortMode.priority:
      items.sort((a, b) {
        final sev = _itemPriorityKey(a).compareTo(_itemPriorityKey(b));
        if (sev != 0) return sev;
        return _itemDate(b).compareTo(_itemDate(a));
      });
    case SortMode.newest:
      items.sort((a, b) => _itemDate(b).compareTo(_itemDate(a)));
  }
}

class _ActivityTab extends ConsumerStatefulWidget {
  const _ActivityTab();
  @override
  ConsumerState<_ActivityTab> createState() => _ActivityTabState();
}

class _ActivityTabState extends ConsumerState<_ActivityTab> {
  @override
  Widget build(BuildContext context) {
    // SSE listener for state changes (open/closed transitions)
    ref.listen(sseStreamProvider, (_, next) {
      next.whenData((event) {
        if (event.type == 'pr_state_changed' ||
            event.type == 'issue_state_changed') {
          ref.invalidate(prsProvider);
          ref.invalidate(issuesProvider);
        }
      });
    });

    final prsAsync = ref.watch(prsProvider);
    final issuesAsync = ref.watch(issuesProvider);
    final sort = ref.watch(reviewsSortProvider);
    final filters = ref.watch(activityFiltersProvider);

    // Combine loading states
    if (prsAsync.isLoading && issuesAsync.isLoading) {
      return const Center(child: CircularProgressIndicator());
    }
    if (prsAsync.hasError && issuesAsync.hasError) {
      return _errorView(context, prsAsync.error!);
    }

    final prs = prsAsync.valueOrNull ?? [];
    final issues = issuesAsync.valueOrNull ?? [];

    // Collect all known repos for the filter bar.
    final allRepos = <String>{
      ...prs.map((p) => p.repo),
      ...issues.map((i) => i.repo),
    }..remove('');

    // Build unified list of items.
    final List<_ActivityItem> items = [
      ...prs.where((p) => p.repo.isNotEmpty).map((p) => _ActivityItem.pr(p)),
      ...issues.map((i) => _ActivityItem.issue(i)),
    ];

    // Apply filters.
    final filtered = items
        .where((item) => _matchesFilters(item, filters))
        .toList();

    // Sort.
    _sortItems(filtered, sort);

    if (prs.isEmpty && issues.isEmpty) {
      return const Center(child: Text('No activity yet'));
    }

    final viewMode = filters.viewMode;

    // Build filter bar + count header (shared between list and grid)
    final header = [
      ActivityFilterBar(
        allRepos: allRepos,
        sort: sort,
        onSortChanged: (mode) =>
            ref.read(reviewsSortProvider.notifier).set(mode),
      ),
      if (filters.hasFilters)
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 0, 16, 4),
          child: Text(
            '${filtered.length} item${filtered.length == 1 ? '' : 's'}',
            style: TextStyle(fontSize: 11, color: Colors.grey.shade500),
          ),
        ),
    ];

    if (filtered.isEmpty && filters.hasFilters) {
      return Column(
        children: [
          ...header,
          const Expanded(
            child: Center(child: Text('No items match the current filters.')),
          ),
        ],
      );
    }

    if (viewMode == 'grid') {
      return Column(
        children: [
          ...header,
          Expanded(
            child: GridView.builder(
              padding: const EdgeInsets.all(8),
              gridDelegate: const SliverGridDelegateWithMaxCrossAxisExtent(
                maxCrossAxisExtent: 300,
                childAspectRatio: 1.6,
                crossAxisSpacing: 8,
                mainAxisSpacing: 8,
              ),
              itemCount: filtered.length,
              itemBuilder: (ctx, i) => _ActivityGridTile(item: filtered[i]),
            ),
          ),
        ],
      );
    }

    // Default: list mode
    return ListView(
      padding: const EdgeInsets.symmetric(vertical: 8),
      children: [
        ...header,
        ...filtered.map(
          (item) => switch (item) {
            _PRItem(:final pr) => _PRTile(pr: pr),
            _IssueItem(:final issue) => _IssueActivityTile(issue: issue),
          },
        ),
      ],
    );
  }

  Widget _errorView(BuildContext context, Object e) {
    final daemonStarting = ref.watch(daemonStartingProvider);
    return Center(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          const Icon(Icons.wifi_off, size: 48, color: Colors.grey),
          const SizedBox(height: 12),
          const Text(
            'Could not reach the Heimdallm daemon.',
            style: TextStyle(fontWeight: FontWeight.w600),
          ),
          const SizedBox(height: 4),
          const Text(
            'Start it here or open Settings to adjust configuration.',
            style: TextStyle(color: Colors.grey),
          ),
          const SizedBox(height: 16),
          Wrap(
            alignment: WrapAlignment.center,
            spacing: 8,
            runSpacing: 8,
            children: [
              TextButton(
                onPressed: () {
                  ref.invalidate(prsProvider);
                  ref.invalidate(issuesProvider);
                },
                child: const Text('Retry'),
              ),
              FilledButton.icon(
                icon: daemonStarting
                    ? const SizedBox(
                        width: 16,
                        height: 16,
                        child: CircularProgressIndicator(
                          strokeWidth: 2,
                          color: Colors.white,
                        ),
                      )
                    : const Icon(Icons.play_arrow, size: 16),
                label: Text(daemonStarting ? 'Starting...' : 'Start Server'),
                onPressed: daemonStarting
                    ? null
                    : () => _startDaemon(context, ref),
              ),
              FilledButton.icon(
                icon: const Icon(Icons.settings, size: 16),
                label: const Text('Settings'),
                onPressed: () => context.push('/config'),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

// ── PR Tile ───────────────────────────────────────────────────────────────────

class _PRTile extends ConsumerStatefulWidget {
  final PR pr;
  const _PRTile({required this.pr});

  @override
  ConsumerState<_PRTile> createState() => _PRTileState();
}

class _PRTileState extends ConsumerState<_PRTile> {
  String get _reviewKey => '${widget.pr.repo}:${widget.pr.number}';

  Future<void> _triggerReview() async {
    // Optimistically mark as reviewing before the SSE event arrives.
    // Baseline = current latestReview.id (0 if none) so reconciliation can
    // later distinguish a stuck key from an in-progress re-review.
    final baseline = widget.pr.latestReview?.id ?? 0;
    ref
        .read(reviewingPRsProvider.notifier)
        .update((s) => {...s, _reviewKey: baseline});
    try {
      await ref.read(apiClientProvider).triggerReview(widget.pr.id);
    } catch (e) {
      ref
          .read(reviewingPRsProvider.notifier)
          .update((s) => Map.of(s)..remove(_reviewKey));
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _dismiss() async {
    final api = ref.read(apiClientProvider);
    try {
      await api.dismissPR(widget.pr.id);
      ref.invalidate(prsProvider);
      if (mounted) {
        showToast(
          context,
          'PR #${widget.pr.number} dismissed',
          duration: const Duration(seconds: 5),
          actionLabel: 'Undo',
          onAction: () async {
            await api.undismissPR(widget.pr.id);
            ref.invalidate(prsProvider);
          },
        );
      }
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  @override
  Widget build(BuildContext context) {
    final pr = widget.pr;
    final reviewed = pr.latestReview != null;
    final isReviewing = ref.watch(reviewingPRsProvider).containsKey(_reviewKey);

    return Opacity(
      opacity: pr.state == 'open' ? 1.0 : 0.6,
      child: Card(
        margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 3),
        child: InkWell(
          borderRadius: BorderRadius.circular(12),
          onTap: () => context.push('/prs/${pr.id}'),
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
            child: Row(
              children: [
                // Severity bar on the left
                Container(
                  width: 4,
                  height: 48,
                  margin: const EdgeInsets.only(right: 12),
                  decoration: BoxDecoration(
                    color: isReviewing
                        ? Theme.of(context).colorScheme.primary
                        : reviewed
                        ? _severityColor(pr.latestReview!.severity)
                        : Colors.grey.shade600,
                    borderRadius: BorderRadius.circular(2),
                  ),
                ),
                // Type badge + state badge
                const Padding(
                  padding: EdgeInsets.only(right: 6),
                  child: TypeBadge(type: 'pr'),
                ),
                const SizedBox(width: 4),
                StateBadge(state: pr.state),
                const SizedBox(width: 4),
                // Title + subtitle
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        pr.title,
                        style: const TextStyle(fontWeight: FontWeight.w600),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                      ),
                      const SizedBox(height: 4),
                      Text(
                        '${pr.repo} · #${pr.number} · ${pr.author}',
                        style: Theme.of(context).textTheme.bodySmall,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                      ),
                    ],
                  ),
                ),
                const SizedBox(width: 12),
                // Trailing: badge/spinner + Review + dismiss — all in one row
                Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    // Status indicator
                    if (isReviewing)
                      SizedBox(
                        width: 18,
                        height: 18,
                        child: CircularProgressIndicator(
                          strokeWidth: 2,
                          color: Theme.of(context).colorScheme.primary,
                        ),
                      )
                    else if (reviewed)
                      SeverityBadge(severity: pr.latestReview!.severity)
                    else
                      _chip('PENDING', Colors.grey.shade700),
                    const SizedBox(width: 8),
                    // Review button (hidden while reviewing)
                    if (!isReviewing)
                      SizedBox(
                        height: 28,
                        child: ElevatedButton(
                          style: ElevatedButton.styleFrom(
                            padding: const EdgeInsets.symmetric(horizontal: 10),
                            textStyle: const TextStyle(fontSize: 12),
                          ),
                          onPressed: _triggerReview,
                          child: const Text('Review'),
                        ),
                      ),
                    // Dismiss
                    IconButton(
                      icon: const Icon(Icons.close, size: 14),
                      tooltip: 'Dismiss PR',
                      color: Colors.grey.shade600,
                      visualDensity: VisualDensity.compact,
                      onPressed: _dismiss,
                    ),
                  ],
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }

  Widget _chip(String label, Color color) => Container(
    padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
    decoration: BoxDecoration(
      color: color,
      borderRadius: BorderRadius.circular(4),
    ),
    child: Text(
      label,
      style: const TextStyle(
        color: Colors.white,
        fontSize: 11,
        fontWeight: FontWeight.w600,
      ),
    ),
  );

  Color _severityColor(String s) {
    switch (s.toLowerCase()) {
      case 'high':
        return Colors.red.shade700;
      case 'medium':
        return Colors.orange.shade700;
      default:
        return Colors.green.shade700;
    }
  }
}

class _IssueActivityTile extends StatelessWidget {
  final TrackedIssue issue;
  const _IssueActivityTile({required this.issue});

  String get _type => _itemType(_IssueItem(issue));

  @override
  Widget build(BuildContext context) {
    final reviewed = issue.latestReview != null;
    final severity = issue.latestReview?.severity ?? '';

    return Opacity(
      opacity: issue.state == 'open' ? 1.0 : 0.6,
      child: Card(
        margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 3),
        child: InkWell(
          borderRadius: BorderRadius.circular(12),
          onTap: () => context.push('/issues/${issue.id}'),
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
            child: Row(
              children: [
                Container(
                  width: 4,
                  height: 48,
                  margin: const EdgeInsets.only(right: 12),
                  decoration: BoxDecoration(
                    color: reviewed
                        ? _severityColor(severity)
                        : Colors.grey.shade600,
                    borderRadius: BorderRadius.circular(2),
                  ),
                ),
                // Type badge + state badge
                Padding(
                  padding: const EdgeInsets.only(right: 6),
                  child: TypeBadge(type: _type),
                ),
                const SizedBox(width: 4),
                StateBadge(state: issue.state),
                const SizedBox(width: 4),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        issue.title,
                        style: const TextStyle(fontWeight: FontWeight.w600),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                      ),
                      const SizedBox(height: 4),
                      Text(
                        '${issue.repo} · #${issue.number} · ${issue.author}',
                        style: Theme.of(context).textTheme.bodySmall,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                      ),
                    ],
                  ),
                ),
                const SizedBox(width: 12),
                if (reviewed)
                  SeverityBadge(severity: severity)
                else
                  Container(
                    padding: const EdgeInsets.symmetric(
                      horizontal: 8,
                      vertical: 3,
                    ),
                    decoration: BoxDecoration(
                      color: Colors.grey.shade700,
                      borderRadius: BorderRadius.circular(4),
                    ),
                    child: const Text(
                      'PENDING',
                      style: TextStyle(
                        color: Colors.white,
                        fontSize: 11,
                        fontWeight: FontWeight.w600,
                      ),
                    ),
                  ),
              ],
            ),
          ),
        ),
      ),
    );
  }

  Color _severityColor(String s) {
    switch (s.toLowerCase()) {
      case 'critical':
        return Colors.red.shade900;
      case 'high':
        return Colors.red.shade700;
      case 'medium':
        return Colors.orange.shade700;
      default:
        return Colors.green.shade700;
    }
  }
}

// ── Grid tile ─────────────────────────────────────────────────────────────────

class _ActivityGridTile extends StatelessWidget {
  final _ActivityItem item;
  const _ActivityGridTile({required this.item});

  @override
  Widget build(BuildContext context) {
    final String type;
    final Color color;
    final String state;
    final String title;
    final String subtitle;
    final String? severity;
    final DateTime timestamp;

    switch (item) {
      case _PRItem(:final pr):
        type = 'PR';
        color = Colors.blue;
        state = pr.state;
        title = pr.title;
        subtitle = '${pr.repo} #${pr.number} · ${pr.author}';
        severity = pr.latestReview?.severity;
        timestamp = pr.updatedAt;
      case _IssueItem(:final issue):
        final isDev = issue.latestReview?.actionTaken == 'auto_implement';
        type = isDev ? 'DEV' : 'IT';
        color = isDev ? Colors.green : Colors.orange;
        state = issue.state;
        title = issue.title;
        subtitle = '${issue.repo} #${issue.number} · ${issue.author}';
        severity = issue.latestReview?.severity;
        timestamp = issue.fetchedAt;
    }

    return Opacity(
      opacity: state == 'open' ? 1.0 : 0.6,
      child: Card(
        child: Padding(
          padding: const EdgeInsets.all(10),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Container(
                    padding: const EdgeInsets.symmetric(
                      horizontal: 5,
                      vertical: 1,
                    ),
                    decoration: BoxDecoration(
                      color: color,
                      borderRadius: BorderRadius.circular(3),
                    ),
                    child: Text(
                      type,
                      style: const TextStyle(
                        color: Colors.white,
                        fontSize: 9,
                        fontWeight: FontWeight.bold,
                      ),
                    ),
                  ),
                  const Spacer(),
                  StateBadge(state: state),
                ],
              ),
              const SizedBox(height: 6),
              Text(
                title,
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: const TextStyle(
                  fontSize: 12,
                  fontWeight: FontWeight.w500,
                ),
              ),
              const SizedBox(height: 4),
              Text(
                subtitle,
                style: TextStyle(fontSize: 10, color: Colors.grey.shade500),
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
              ),
              const Spacer(),
              Row(
                children: [
                  if (severity != null) SeverityBadge(severity: severity),
                  const Spacer(),
                  Text(
                    _timeAgo(timestamp),
                    style: TextStyle(fontSize: 10, color: Colors.grey.shade600),
                  ),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }

  static String _timeAgo(DateTime dt) {
    final diff = DateTime.now().difference(dt);
    if (diff.inMinutes < 60) return '${diff.inMinutes}m ago';
    if (diff.inHours < 24) return '${diff.inHours}h ago';
    return '${diff.inDays}d ago';
  }
}
