import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:shared_preferences/shared_preferences.dart';
import '../../core/api/api_client.dart';
import '../../core/instances/aggregation.dart';
import '../../core/instances/instances_providers.dart';
import '../instances/widgets/instance_badge.dart';
import '../instances/widgets/instance_selector.dart';
import '../../core/models/pr.dart';
import '../../core/models/review_status.dart';
import '../../core/models/tracked_issue.dart';
import '../../shared/widgets/keep_alive_tab.dart';
import '../../shared/widgets/attention_badge.dart';
import '../../shared/widgets/pr_review_state_badge.dart';
import '../../shared/widgets/severity_badge.dart';
import '../../shared/widgets/state_badge.dart';
import '../../shared/widgets/toast.dart';
import '../../shared/widgets/type_badge.dart';
import '../activity/activity_screen.dart';
import '../activity/activity_providers.dart';
import '../activity/add_pr_dialog.dart';
import '../agents/agents_screen.dart';
import '../circuit_breaker/circuit_breaker_banner.dart';
import '../cli_agents/cli_agents_screen.dart';
import '../config/config_providers.dart';
import '../issues/issues_providers.dart';
import '../merge_tracking/merge_tracking_providers.dart';
import '../merge_tracking/merge_tracking_screen.dart';
import '../repositories/repos_screen.dart';
import '../organizations/orgs_screen.dart';
import '../stats/stats_screen.dart';
import '../updates/check_for_updates_button.dart';
import 'activity_filter_bar.dart';
import 'activity_filters.dart';
import 'dashboard_providers.dart';
import '../server/server_actions.dart' as server_actions;

class DashboardScreen extends ConsumerWidget {
  const DashboardScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final cbMessage = ref.watch(circuitBreakerProvider);
    final daemonRunning = ref.watch(daemonHealthProvider).value ?? false;
    final daemonStarting = ref.watch(daemonStartingProvider);
    final connection = daemonRunning
        ? ref.watch(daemonConnectionProvider)
        : null;
    // Surfaced on the Merge tab so a PR held up by CI is visible without
    // opening the tab at all.
    final checkProblems = ref.watch(mergeTrackingCheckProblemCountProvider);
    return DefaultTabController(
      length: 8,
      child: Scaffold(
        appBar: AppBar(
          title: const Text('Heimdallm'),
          actions: [
            // Renders nothing unless more than one instance is registered, so a
            // single-daemon install sees exactly the toolbar it always had.
            const InstanceSelector(),
            const CheckForUpdatesButton(),
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
              icon: const Icon(Icons.dns_outlined),
              tooltip: 'Server',
              onPressed: () => context.push('/server'),
            ),
            IconButton(
              icon: const Icon(Icons.settings),
              onPressed: () => context.push('/config'),
            ),
            IconButton(
              icon: const Icon(Icons.refresh),
              onPressed: () {
                // Invalidate the aggregating providers: the flat lists derive
                // from them, so refreshing only the derived ones would replay
                // the same cached fan-out.
                ref.invalidate(daemonInstancesProvider);
                ref.invalidate(prsByInstanceProvider);
                ref.invalidate(issuesByInstanceProvider);
                ref.invalidate(mergeTrackingByInstanceProvider);
                ref.invalidate(statsByInstanceProvider);
                ref.invalidate(githubRateLimitProvider);
                ref.invalidate(activityEntriesProvider);
                ref.invalidate(activityOptionsProvider);
              },
            ),
          ],
          bottom: TabBar(
            tabs: [
              const Tab(icon: Icon(Icons.dashboard), text: 'Activity'),
              const Tab(icon: Icon(Icons.timeline), text: 'Activity log'),
              Tab(
                icon: _MergeTabIcon(count: checkProblems),
                text: 'Merge',
              ),
              const Tab(
                icon: Icon(Icons.folder_outlined),
                text: 'Repositories',
              ),
              const Tab(
                icon: Icon(Icons.business_outlined),
                text: 'Organizations',
              ),
              const Tab(icon: Icon(Icons.auto_awesome), text: 'Prompts'),
              const Tab(icon: Icon(Icons.smart_toy), text: 'Agents'),
              const Tab(icon: Icon(Icons.bar_chart), text: 'Stats'),
            ],
          ),
        ),
        body: Column(
          children: [
            const AppUpdateBanner(),
            if (cbMessage != null)
              CircuitBreakerBanner(
                message: cbMessage,
                onDismiss: () =>
                    ref.read(circuitBreakerProvider.notifier).set(null),
              ),
            if (connection != null &&
                connection.phase != DaemonConnectionPhase.connected)
              _ConnectionBanner(
                status: connection,
                onRestart: () => server_actions.restartDaemon(context, ref),
              ),
            InstanceFailureBanner(
              failureLabels: ref
                  .watch(instanceReadFailuresProvider)
                  .map((f) => f.label)
                  .toList(),
            ),
            const Expanded(
              child: TabBarView(
                children: [
                  KeepAliveTab(child: _ActivityTab()),
                  KeepAliveTab(child: ActivityScreen()),
                  KeepAliveTab(child: MergeTrackingScreen()),
                  KeepAliveTab(child: ReposScreen()),
                  KeepAliveTab(child: OrgsScreen()),
                  KeepAliveTab(child: AgentsScreen()),
                  KeepAliveTab(child: CLIAgentsScreen()),
                  KeepAliveTab(child: StatsScreen()),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}

class _ConnectionBanner extends StatelessWidget {
  const _ConnectionBanner({required this.status, required this.onRestart});

  final DaemonConnectionStatus status;
  final VoidCallback onRestart;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final (color, icon, label) = switch (status.phase) {
      DaemonConnectionPhase.connected => (
        Colors.green,
        Icons.check_circle_outline,
        'Connected',
      ),
      DaemonConnectionPhase.stale => (
        Colors.amber,
        Icons.sync_problem,
        'No events received — reconnecting',
      ),
      DaemonConnectionPhase.offline => (
        theme.colorScheme.error,
        Icons.error_outline,
        'Server unavailable',
      ),
      DaemonConnectionPhase.connecting => (
        Colors.blueGrey,
        Icons.sync,
        'Connecting',
      ),
    };
    return Material(
      color: color.withValues(alpha: 0.10),
      child: SafeArea(
        bottom: false,
        child: ConstrainedBox(
          constraints: const BoxConstraints(minHeight: 36),
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 6),
            child: Row(
              children: [
                Icon(icon, size: 18, color: color),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(
                    label,
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: color,
                      fontWeight: FontWeight.w600,
                    ),
                  ),
                ),
                if (status.phase == DaemonConnectionPhase.offline)
                  TextButton(
                    onPressed: onRestart,
                    child: const Text('Restart'),
                  ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

Future<void> _confirmShutdown(BuildContext context, WidgetRef ref) =>
    server_actions.confirmShutdown(context, ref);

Future<void> _startDaemon(BuildContext context, WidgetRef ref) =>
    server_actions.startDaemon(context, ref);

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
  const _ActivityItem({this.instanceId = '', this.instanceName = ''});

  /// Which instance served this row. Empty on a single-daemon install, where
  /// no badge is rendered at all.
  final String instanceId;
  final String instanceName;
}

class _PRItem extends _ActivityItem {
  final PR pr;
  const _PRItem(this.pr, {super.instanceId, super.instanceName});
}

class _IssueItem extends _ActivityItem {
  final TrackedIssue issue;
  const _IssueItem(this.issue, {super.instanceId, super.instanceName});
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
          ref.invalidate(prsByInstanceProvider);
          ref.invalidate(issuesByInstanceProvider);
        }
      });
    });

    // Watch the aggregating providers so every row knows which instance
    // served it; the flat lists below are just their values.
    final prsAsync = ref.watch(prsByInstanceProvider);
    final issuesAsync = ref.watch(issuesByInstanceProvider);
    final sort = ref.watch(reviewsSortProvider);
    final filters = ref.watch(activityFiltersProvider);
    final emptyToolbar = ActivityFilterBar(
      allRepos: const {},
      sort: sort,
      onSortChanged: (mode) => ref.read(reviewsSortProvider.notifier).set(mode),
      onAddPR: () => showAddPRDialog(context),
    );

    // Combine loading states
    if (prsAsync.isLoading && issuesAsync.isLoading) {
      return Column(
        children: [
          emptyToolbar,
          const Expanded(child: Center(child: CircularProgressIndicator())),
        ],
      );
    }
    if (prsAsync.hasError && issuesAsync.hasError) {
      return Column(
        children: [
          emptyToolbar,
          Expanded(child: _errorView(context, prsAsync.error!)),
        ],
      );
    }

    final prScoped = prsAsync.value?.items ?? const <InstanceScoped<PR>>[];
    final issueScoped =
        issuesAsync.value?.items ?? const <InstanceScoped<TrackedIssue>>[];
    final prs = prScoped.map((e) => e.value).toList();
    final issues = issueScoped.map((e) => e.value).toList();

    // Collect all known repos for the filter bar.
    final allRepos = <String>{
      ...prs.map((p) => p.repo),
      ...issues.map((i) => i.repo),
    }..remove('');

    // Build unified list of items.
    final List<_ActivityItem> items = [
      ...prScoped
          .where((e) => e.value.repo.isNotEmpty)
          .map(
            (e) => _PRItem(
              e.value,
              instanceId: e.instanceId,
              instanceName: e.instanceName,
            ),
          ),
      ...issueScoped.map(
        (e) => _IssueItem(
          e.value,
          instanceId: e.instanceId,
          instanceName: e.instanceName,
        ),
      ),
    ];

    // Apply filters.
    final filtered = items
        .where((item) => _matchesFilters(item, filters))
        .toList();

    // Sort.
    _sortItems(filtered, sort);

    final viewMode = filters.viewMode;

    // Build filter bar + count header (shared between list and grid)
    final header = [
      ActivityFilterBar(
        allRepos: allRepos,
        sort: sort,
        onSortChanged: (mode) =>
            ref.read(reviewsSortProvider.notifier).set(mode),
        onAddPR: () => showAddPRDialog(context),
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

    if (prs.isEmpty && issues.isEmpty) {
      return Column(
        children: [
          ...header,
          const Expanded(child: Center(child: Text('No activity yet'))),
        ],
      );
    }

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
            _PRItem(:final pr, :final instanceId, :final instanceName) =>
              _PRTile(
                pr: pr,
                instanceId: instanceId,
                instanceName: instanceName,
              ),
            _IssueItem(:final issue, :final instanceId) =>
              _IssueActivityTile(issue: issue, instanceId: instanceId),
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
                  ref.invalidate(prsByInstanceProvider);
                  ref.invalidate(issuesByInstanceProvider);
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
  final String instanceId;
  final String instanceName;
  const _PRTile({
    required this.pr,
    this.instanceId = '',
    this.instanceName = '',
  });

  @override
  ConsumerState<_PRTile> createState() => _PRTileState();
}

class _PRTileState extends ConsumerState<_PRTile> {
  String get _reviewKey =>
      reviewKeyFor(widget.instanceId, widget.pr.repo, widget.pr.number);

  /// The client for the instance holding this PR, not whichever one the
  /// dashboard is currently scoped to.
  ApiClient get _api => clientForInstanceOf(ref, widget.instanceId);
  bool _cancelling = false;

  Future<void> _triggerReview() async {
    // Optimistically mark as reviewing before the SSE event arrives.
    // Baseline = current latestReview.id (0 if none) so reconciliation can
    // later distinguish a stuck key from an in-progress re-review.
    final baseline = widget.pr.latestReview?.id ?? 0;
    ref
        .read(reviewingPRsProvider.notifier)
        .update((s) => {...s, _reviewKey: baseline});
    try {
      await _api.triggerReview(widget.pr.id);
    } catch (e) {
      ref
          .read(reviewingPRsProvider.notifier)
          .update((s) => Map.of(s)..remove(_reviewKey));
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _dismiss() async {
    final api = _api;
    try {
      await api.dismissPR(widget.pr.id);
      ref.invalidate(prsByInstanceProvider);
      if (mounted) {
        showToast(
          context,
          'PR #${widget.pr.number} dismissed',
          duration: const Duration(seconds: 5),
          actionLabel: 'Undo',
          onAction: () async {
            await api.undismissPR(widget.pr.id);
            ref.invalidate(prsByInstanceProvider);
          },
        );
      }
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _cancelReview() async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (dialogContext) => AlertDialog(
        title: const Text('Cancel this review?'),
        content: Text(
          'The active agent process for ${widget.pr.repo} #${widget.pr.number} '
          'will be terminated. Other reviews will continue running.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(dialogContext, false),
            child: const Text('Keep running'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(dialogContext, true),
            child: const Text('Cancel review'),
          ),
        ],
      ),
    );
    if (confirmed != true || !mounted) return;
    setState(() => _cancelling = true);
    try {
      await _api.cancelReview(widget.pr.id);
      if (mounted) showToast(context, 'Cancellation requested');
    } catch (e) {
      if (mounted) {
        setState(() => _cancelling = false);
        showToast(context, 'Error: $e', isError: true);
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final pr = widget.pr;
    final reviewed = pr.latestReview != null;
    final status = pr.reviewStatus;
    final failure = status != null && !status.active && status.error.isNotEmpty
        ? status
        : null;
    final isReviewing =
        (status?.active ?? false) ||
        ref.watch(reviewingPRsProvider).containsKey(_reviewKey);

    return Opacity(
      opacity: pr.state == 'open' ? 1.0 : 0.6,
      child: Card(
        margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 3),
        child: InkWell(
          borderRadius: BorderRadius.circular(12),
          onTap: () => context.push(prDetailRoute(pr.id, widget.instanceId)),
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
            child: Row(
              children: [
                // Severity bar on the left
                Container(
                  width: 4,
                  height: failure == null ? 48 : 62,
                  margin: const EdgeInsets.only(right: 12),
                  decoration: BoxDecoration(
                    color: isReviewing
                        ? Theme.of(context).colorScheme.primary
                        : failure != null
                        ? Theme.of(context).colorScheme.error
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
                      Row(
                        children: [
                          Flexible(
                            child: Text(
                              '${pr.repo} · #${pr.number} · ${pr.author}',
                              style: Theme.of(context).textTheme.bodySmall,
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                            ),
                          ),
                          if (widget.instanceId.isNotEmpty) ...[
                            const SizedBox(width: 6),
                            InstanceBadge(
                              instanceId: widget.instanceId,
                              instanceName: widget.instanceName,
                              compact: true,
                            ),
                          ],
                        ],
                      ),
                      if (failure != null && !isReviewing) ...[
                        const SizedBox(height: 3),
                        Tooltip(
                          message: failure.error,
                          child: Text(
                            reviewFailureSummary(failure),
                            style: Theme.of(context).textTheme.bodySmall
                                ?.copyWith(
                                  color: Theme.of(context).colorScheme.error,
                                ),
                            maxLines: 2,
                            overflow: TextOverflow.ellipsis,
                          ),
                        ),
                      ],
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
                    else if (failure != null)
                      Tooltip(
                        message: failure.error,
                        child: _chip(
                          failure.isCancelled ? 'CANCELLED' : 'FAILED',
                          Theme.of(context).colorScheme.error,
                        ),
                      )
                    else if (reviewed)
                      SeverityBadge(severity: pr.latestReview!.severity)
                    else
                      _chip('PENDING', Colors.grey.shade700),
                    const SizedBox(width: 8),
                    if (isReviewing)
                      SizedBox(
                        height: 28,
                        child: OutlinedButton.icon(
                          icon: _cancelling
                              ? const SizedBox(
                                  width: 12,
                                  height: 12,
                                  child: CircularProgressIndicator(
                                    strokeWidth: 2,
                                  ),
                                )
                              : const Icon(
                                  Icons.stop_circle_outlined,
                                  size: 15,
                                ),
                          label: Text(_cancelling ? 'Cancelling…' : 'Cancel'),
                          onPressed: _cancelling ? null : _cancelReview,
                        ),
                      )
                    else
                      SizedBox(
                        height: 28,
                        child: ElevatedButton(
                          style: ElevatedButton.styleFrom(
                            padding: const EdgeInsets.symmetric(horizontal: 10),
                            textStyle: const TextStyle(fontSize: 12),
                          ),
                          onPressed: _triggerReview,
                          child: Text(failure == null ? 'Review' : 'Retry'),
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

class _IssueActivityTile extends ConsumerStatefulWidget {
  final TrackedIssue issue;
  final String instanceId;
  const _IssueActivityTile({required this.issue, this.instanceId = ''});

  @override
  ConsumerState<_IssueActivityTile> createState() => _IssueActivityTileState();
}

class _IssueActivityTileState extends ConsumerState<_IssueActivityTile> {
  String get _type => _itemType(_IssueItem(widget.issue));

  /// The client for the instance holding this issue.
  ApiClient get _api => clientForInstanceOf(ref, widget.instanceId);

  Future<void> _dismiss() async {
    final api = _api;
    try {
      await api.dismissIssue(widget.issue.id);
      ref.invalidate(issuesByInstanceProvider);
      if (mounted) {
        showToast(
          context,
          'Issue #${widget.issue.number} dismissed',
          duration: const Duration(seconds: 5),
          actionLabel: 'Undo',
          onAction: () async {
            await api.undismissIssue(widget.issue.id);
            ref.invalidate(issuesByInstanceProvider);
          },
        );
      }
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  @override
  Widget build(BuildContext context) {
    final issue = widget.issue;
    final reviewed = issue.latestReview != null;
    final severity = issue.latestReview?.severity ?? '';
    // auto_implement_no_changes is a terminal "needs attention" state —
    // the review row has an empty triage block (severity defaults to
    // LOW/green), which would otherwise misrepresent it as a clean low
    // severity result. See #483.
    final needsAttention =
        issue.latestReview?.actionTaken == 'auto_implement_no_changes';

    return Opacity(
      opacity: issue.state == 'open' ? 1.0 : 0.6,
      child: Card(
        margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 3),
        child: InkWell(
          borderRadius: BorderRadius.circular(12),
          onTap: () =>
              context.push(issueDetailRoute(issue.id, widget.instanceId)),
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
            child: Row(
              children: [
                Container(
                  width: 4,
                  height: 48,
                  margin: const EdgeInsets.only(right: 12),
                  decoration: BoxDecoration(
                    color: needsAttention
                        ? Colors.deepOrange.shade700
                        : reviewed
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
                // Trailing: severity/PENDING badge + dismiss — mirrors _PRTile.
                Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    if (issue.linkedPR != null &&
                        issue.linkedPR!.externalReviewState.isNotEmpty) ...[
                      PRReviewStateBadge(
                        state: issue.linkedPR!.externalReviewState,
                      ),
                      const SizedBox(width: 6),
                    ],
                    if (needsAttention)
                      const AttentionBadge()
                    else if (reviewed)
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
                    IconButton(
                      icon: const Icon(Icons.close, size: 14),
                      tooltip: 'Dismiss issue',
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
    final bool needsAttention;
    final DateTime timestamp;

    switch (item) {
      case _PRItem(:final pr):
        type = 'PR';
        color = Colors.blue;
        state = pr.state;
        title = pr.title;
        subtitle = '${pr.repo} #${pr.number} · ${pr.author}';
        severity = pr.latestReview?.severity;
        needsAttention = false;
        timestamp = pr.updatedAt;
      case _IssueItem(:final issue):
        final isDev = issue.latestReview?.actionTaken == 'auto_implement';
        type = isDev ? 'DEV' : 'IT';
        color = isDev ? Colors.green : Colors.orange;
        state = issue.state;
        title = issue.title;
        subtitle = '${issue.repo} #${issue.number} · ${issue.author}';
        // Terminal no-changes rows have an empty triage block — render
        // them as a NEEDS ATTENTION chip instead of a misleading green
        // LOW badge (#483).
        needsAttention =
            issue.latestReview?.actionTaken == 'auto_implement_no_changes';
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
                  if (needsAttention)
                    const AttentionBadge()
                  else if (severity != null)
                    SeverityBadge(severity: severity),
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

/// The Merge tab's icon, badged with the number of tracked PRs whose merge is
/// held up by CI.
///
/// The badge exists so a failing check on your own PR is visible from any tab —
/// the whole point of the feature is that you stop having to go looking.
class _MergeTabIcon extends StatelessWidget {
  final int count;

  const _MergeTabIcon({required this.count});

  @override
  Widget build(BuildContext context) {
    if (count == 0) return const Icon(Icons.merge_type);
    return Badge.count(
      count: count,
      backgroundColor: Theme.of(context).colorScheme.error,
      child: const Icon(Icons.merge_type),
    );
  }
}
