import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:shared_preferences/shared_preferences.dart';
import '../../core/models/pr.dart';
import '../../core/models/tracked_issue.dart';
import '../../shared/widgets/severity_badge.dart';
import '../../shared/widgets/toast.dart';
import '../../shared/widgets/type_badge.dart';
import '../activity/activity_screen.dart';
import '../activity/activity_providers.dart';
import '../agents/agents_screen.dart';
import '../cli_agents/cli_agents_screen.dart';
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
    return DefaultTabController(
      length: 6,
      child: Scaffold(
        appBar: AppBar(
          title: const Text('Heimdallm'),
          actions: [
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
              Tab(icon: Icon(Icons.dashboard),       text: 'Activity'),
              Tab(icon: Icon(Icons.timeline),        text: 'Activity log'),
              Tab(icon: Icon(Icons.folder_outlined), text: 'Repositories'),
              Tab(icon: Icon(Icons.auto_awesome),    text: 'Prompts'),
              Tab(icon: Icon(Icons.smart_toy),       text: 'Agents'),
              Tab(icon: Icon(Icons.bar_chart),       text: 'Stats'),
            ],
          ),
        ),
        body: const TabBarView(
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
    );
  }
}

// ── Reviews tab ──────────────────────────────────────────────────────────────

enum _SortMode { priority, newest }

const _sortPrefKey = 'activity_sort_mode';

/// Notifier that owns both the sort state and its SharedPreferences persistence.
class _SortNotifier extends Notifier<_SortMode> {
  @override
  _SortMode build() {
    _loadAsync();
    return _SortMode.priority;
  }

  void _loadAsync() async {
    try {
      final prefs = await SharedPreferences.getInstance();
      final value = prefs.getString(_sortPrefKey);
      if (value == 'newest') {
        state = _SortMode.newest;
      }
    } catch (e) {
      debugPrint('SortNotifier: failed to load preference: $e');
    }
  }

  void set(_SortMode mode) {
    state = mode;
    SharedPreferences.getInstance().then((prefs) {
      prefs.setString(_sortPrefKey, mode.name);
    }).catchError((e) {
      debugPrint('SortNotifier: failed to save preference: $e');
    });
  }
}

final _reviewsSortProvider =
    NotifierProvider<_SortNotifier, _SortMode>(_SortNotifier.new);

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
  _PRItem(:final pr)       => pr.repo,
  _IssueItem(:final issue) => issue.repo,
};

String _itemTitle(_ActivityItem item) => switch (item) {
  _PRItem(:final pr)       => pr.title,
  _IssueItem(:final issue) => issue.title,
};

int _itemNumber(_ActivityItem item) => switch (item) {
  _PRItem(:final pr)       => pr.number,
  _IssueItem(:final issue) => issue.number,
};

String _itemAuthor(_ActivityItem item) => switch (item) {
  _PRItem(:final pr)       => pr.author,
  _IssueItem(:final issue) => issue.author,
};

DateTime _itemDate(_ActivityItem item) => switch (item) {
  _PRItem(:final pr)       => pr.updatedAt,
  _IssueItem(:final issue) => issue.latestReview?.createdAt ?? issue.fetchedAt,
};

int _itemPriorityKey(_ActivityItem item) => switch (item) {
  _PRItem(:final pr) => pr.latestReview == null
      ? 0
      : switch (pr.latestReview!.severity.toLowerCase()) {
          'high'   => 1,
          'medium' => 2,
          _        => 3,
        },
  _IssueItem(:final issue) => issue.latestReview == null
      ? 0
      : switch (issue.latestReview!.severity.toLowerCase()) {
          'critical' => 0,
          'high'     => 1,
          'medium'   => 2,
          _          => 3,
        },
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
  // Search
  if (filters.search.isNotEmpty) {
    final q = filters.search.toLowerCase();
    final title  = _itemTitle(item).toLowerCase();
    final repo   = _itemRepo(item).toLowerCase();
    final number = _itemNumber(item).toString();
    final author = _itemAuthor(item).toLowerCase();
    if (!title.contains(q) && !repo.contains(q) && !number.contains(q) && !author.contains(q)) {
      return false;
    }
  }
  return true;
}

void _sortItems(List<_ActivityItem> items, _SortMode mode) {
  switch (mode) {
    case _SortMode.priority:
      items.sort((a, b) {
        final sev = _itemPriorityKey(a).compareTo(_itemPriorityKey(b));
        if (sev != 0) return sev;
        return _itemDate(b).compareTo(_itemDate(a));
      });
    case _SortMode.newest:
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
    final prsAsync    = ref.watch(prsProvider);
    final issuesAsync = ref.watch(issuesProvider);
    final sort        = ref.watch(_reviewsSortProvider);
    final filters     = ref.watch(activityFiltersProvider);

    // Combine loading states
    if (prsAsync.isLoading && issuesAsync.isLoading) {
      return const Center(child: CircularProgressIndicator());
    }
    if (prsAsync.hasError && issuesAsync.hasError) {
      return _errorView(context, prsAsync.error!);
    }

    final prs    = prsAsync.valueOrNull ?? [];
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
    final filtered = items.where((item) => _matchesFilters(item, filters)).toList();

    // Sort.
    _sortItems(filtered, sort);

    if (prs.isEmpty && issues.isEmpty) {
      return const Center(child: Text('No activity yet'));
    }

    return ListView(
      padding: const EdgeInsets.symmetric(vertical: 8),
      children: [
        // Sort + Filter bar — single unified row
        ActivityFilterBar(
          allRepos: allRepos,
          sort: sort == _SortMode.priority ? SortMode.priority : SortMode.newest,
          onSortChanged: (mode) {
            final internal = mode == SortMode.priority ? _SortMode.priority : _SortMode.newest;
            ref.read(_reviewsSortProvider.notifier).set(internal);
          },
        ),
        // Filtered count when filters are active
        if (filters.hasFilters)
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 0, 16, 4),
            child: Text(
              '${filtered.length} item${filtered.length == 1 ? '' : 's'}',
              style: TextStyle(fontSize: 11, color: Colors.grey.shade500),
            ),
          ),
        if (filtered.isEmpty && filters.hasFilters)
          const Padding(
            padding: EdgeInsets.symmetric(vertical: 32),
            child: Center(child: Text('No items match the current filters.')),
          )
        else
          ...filtered.map((item) => switch (item) {
            _PRItem(:final pr) => _PRTile(pr: pr),
            _IssueItem(:final issue) => _IssueActivityTile(issue: issue),
          }),
      ],
    );
  }

  Widget _errorView(BuildContext context, Object e) {
    return Center(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          const Icon(Icons.wifi_off, size: 48, color: Colors.grey),
          const SizedBox(height: 12),
          const Text('Could not reach the Heimdallm daemon.',
              style: TextStyle(fontWeight: FontWeight.w600)),
          const SizedBox(height: 4),
          const Text('Go to Settings to configure and start it.',
              style: TextStyle(color: Colors.grey)),
          const SizedBox(height: 16),
          Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              TextButton(
                  onPressed: () {
                    ref.invalidate(prsProvider);
                    ref.invalidate(issuesProvider);
                  },
                  child: const Text('Retry')),
              const SizedBox(width: 8),
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
    // Optimistically mark as reviewing before the SSE event arrives
    ref.read(reviewingPRsProvider.notifier).update((s) => {...s, _reviewKey});
    try {
      await ref.read(apiClientProvider).triggerReview(widget.pr.id);
    } catch (e) {
      ref.read(reviewingPRsProvider.notifier).update((s) => s.difference({_reviewKey}));
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _dismiss() async {
    final api = ref.read(apiClientProvider);
    try {
      await api.dismissPR(widget.pr.id);
      ref.invalidate(prsProvider);
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(
          duration: const Duration(seconds: 5),
          showCloseIcon: true,
          content: Text('PR #${widget.pr.number} dismissed'),
          action: SnackBarAction(
            label: 'Undo',
            onPressed: () async {
              await api.undismissPR(widget.pr.id);
              ref.invalidate(prsProvider);
            },
          ),
        ));
      }
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  @override
  Widget build(BuildContext context) {
    final pr = widget.pr;
    final reviewed = pr.latestReview != null;
    final isReviewing = ref.watch(reviewingPRsProvider).contains(_reviewKey);

    return Card(
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
                width: 4, height: 48,
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
              // Type badge
              const Padding(
                padding: EdgeInsets.only(right: 10),
                child: TypeBadge(type: 'pr'),
              ),
              // Title + subtitle
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(pr.title,
                        style: const TextStyle(fontWeight: FontWeight.w600),
                        maxLines: 1, overflow: TextOverflow.ellipsis),
                    const SizedBox(height: 4),
                    Text('${pr.repo} · #${pr.number} · ${pr.author}',
                        style: Theme.of(context).textTheme.bodySmall,
                        maxLines: 1, overflow: TextOverflow.ellipsis),
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
                      width: 18, height: 18,
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
                            textStyle: const TextStyle(fontSize: 12)),
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
    );
  }

  Widget _chip(String label, Color color) => Container(
    padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
    decoration: BoxDecoration(color: color, borderRadius: BorderRadius.circular(4)),
    child: Text(label,
        style: const TextStyle(
            color: Colors.white, fontSize: 11, fontWeight: FontWeight.w600)),
  );

  Color _severityColor(String s) {
    switch (s.toLowerCase()) {
      case 'high':   return Colors.red.shade700;
      case 'medium': return Colors.orange.shade700;
      default:       return Colors.green.shade700;
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

    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 3),
      child: InkWell(
        borderRadius: BorderRadius.circular(12),
        onTap: () => context.push('/issues/${issue.id}'),
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
          child: Row(
            children: [
              Container(
                width: 4, height: 48,
                margin: const EdgeInsets.only(right: 12),
                decoration: BoxDecoration(
                  color: reviewed ? _severityColor(severity) : Colors.grey.shade600,
                  borderRadius: BorderRadius.circular(2),
                ),
              ),
              // Type badge
              Padding(
                padding: const EdgeInsets.only(right: 10),
                child: TypeBadge(type: _type),
              ),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(issue.title,
                        style: const TextStyle(fontWeight: FontWeight.w600),
                        maxLines: 1, overflow: TextOverflow.ellipsis),
                    const SizedBox(height: 4),
                    Text('${issue.repo} · #${issue.number} · ${issue.author}',
                        style: Theme.of(context).textTheme.bodySmall,
                        maxLines: 1, overflow: TextOverflow.ellipsis),
                  ],
                ),
              ),
              const SizedBox(width: 12),
              if (reviewed)
                SeverityBadge(severity: severity)
              else
                Container(
                  padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
                  decoration: BoxDecoration(
                      color: Colors.grey.shade700,
                      borderRadius: BorderRadius.circular(4)),
                  child: const Text('PENDING',
                      style: TextStyle(
                          color: Colors.white, fontSize: 11, fontWeight: FontWeight.w600)),
                ),
            ],
          ),
        ),
      ),
    );
  }

  Color _severityColor(String s) {
    switch (s.toLowerCase()) {
      case 'critical': return Colors.red.shade900;
      case 'high':     return Colors.red.shade700;
      case 'medium':   return Colors.orange.shade700;
      default:         return Colors.green.shade700;
    }
  }
}
