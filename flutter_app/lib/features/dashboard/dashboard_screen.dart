import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../../core/models/pr.dart';
import '../../core/models/tracked_issue.dart';
import '../../shared/widgets/severity_badge.dart';
import '../../shared/widgets/toast.dart';
import '../agents/agents_screen.dart';
import '../cli_agents/cli_agents_screen.dart';
import '../issues/issues_screen.dart';
import '../issues/issues_providers.dart';
import '../repositories/repos_screen.dart';
import '../stats/stats_screen.dart';
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
              },
            ),
          ],
          bottom: const TabBar(
            tabs: [
              Tab(icon: Icon(Icons.dashboard),       text: 'Activity'),
              Tab(icon: Icon(Icons.bug_report),      text: 'Issues'),
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
            IssuesScreen(),
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

/// Persists the user's sort selection across tab navigation.
final _reviewsSortProvider = StateProvider<_SortMode>((ref) => _SortMode.priority);

/// Sort by priority: pending → high → medium → low, then most recent first within group.
int _prSortKey(PR p) {
  if (p.latestReview == null) return 0;
  switch (p.latestReview!.severity.toLowerCase()) {
    case 'high':   return 1;
    case 'medium': return 2;
    default:       return 3;
  }
}

List<PR> _sortedPRs(List<PR> prs, _SortMode mode) {
  final list = [...prs];
  switch (mode) {
    case _SortMode.priority:
      list.sort((a, b) {
        final sev = _prSortKey(a).compareTo(_prSortKey(b));
        if (sev != 0) return sev;
        return b.updatedAt.compareTo(a.updatedAt);
      });
    case _SortMode.newest:
      list.sort((a, b) => b.updatedAt.compareTo(a.updatedAt));
  }
  return list;
}

class _ActivityTab extends ConsumerStatefulWidget {
  const _ActivityTab();
  @override
  ConsumerState<_ActivityTab> createState() => _ActivityTabState();
}

class _ActivityTabState extends ConsumerState<_ActivityTab> {
  bool _reviewsExpanded = true;
  bool _prsExpanded     = true;
  bool _issuesExpanded  = true;

  @override
  Widget build(BuildContext context) {
    final prsAsync    = ref.watch(prsProvider);
    final issuesAsync = ref.watch(issuesProvider);
    final meAsync     = ref.watch(meProvider);
    final sort        = ref.watch(_reviewsSortProvider);

    // Combine loading states
    if (prsAsync.isLoading && issuesAsync.isLoading) {
      return const Center(child: CircularProgressIndicator());
    }
    if (prsAsync.hasError && issuesAsync.hasError) {
      return _errorView(context, prsAsync.error!);
    }

    final prs    = prsAsync.valueOrNull ?? [];
    final issues = issuesAsync.valueOrNull ?? [];
    final me     = meAsync.valueOrNull ?? '';

    final myReviews = _sortedPRs(prs.where((p) =>
        p.repo.isNotEmpty && p.author.toLowerCase() != me.toLowerCase()).toList(), sort);
    final myPRs = _sortedPRs(prs.where((p) =>
        p.repo.isNotEmpty && p.author.toLowerCase() == me.toLowerCase()).toList(), sort);

    if (prs.isEmpty && issues.isEmpty) {
      return const Center(child: Text('No activity yet'));
    }

    return ListView(
      padding: const EdgeInsets.symmetric(vertical: 8),
      children: [
        // Sort selector
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 8, 16, 4),
          child: Row(
            children: [
              Text('Sort:',
                  style: TextStyle(fontSize: 12, color: Colors.grey.shade500)),
              const SizedBox(width: 8),
              _SortButton(
                label: 'Priority',
                icon: Icons.sort,
                selected: sort == _SortMode.priority,
                onTap: () => ref.read(_reviewsSortProvider.notifier).state = _SortMode.priority,
              ),
              const SizedBox(width: 6),
              _SortButton(
                label: 'Newest',
                icon: Icons.schedule,
                selected: sort == _SortMode.newest,
                onTap: () => ref.read(_reviewsSortProvider.notifier).state = _SortMode.newest,
              ),
            ],
          ),
        ),
        if (myReviews.isNotEmpty) ...[
          _CollapseHeader(
            title: 'My Reviews',
            count: myReviews.length,
            expanded: _reviewsExpanded,
            onToggle: () => setState(() => _reviewsExpanded = !_reviewsExpanded),
          ),
          if (_reviewsExpanded)
            ...myReviews.map((pr) => _PRTile(pr: pr)),
        ],
        if (myPRs.isNotEmpty) ...[
          _CollapseHeader(
            title: 'My PRs',
            count: myPRs.length,
            expanded: _prsExpanded,
            onToggle: () => setState(() => _prsExpanded = !_prsExpanded),
          ),
          if (_prsExpanded)
            ...myPRs.map((pr) => _PRTile(pr: pr)),
        ],
        if (issues.isNotEmpty) ...[
          _CollapseHeader(
            title: 'Tracked Issues',
            count: issues.length,
            expanded: _issuesExpanded,
            onToggle: () => setState(() => _issuesExpanded = !_issuesExpanded),
          ),
          if (_issuesExpanded)
            ...issues.map((issue) => _IssueActivityTile(issue: issue)),
        ],
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

// ── Collapsible section header ────────────────────────────────────────────────

// ── Sort button ───────────────────────────────────────────────────────────────

class _SortButton extends StatelessWidget {
  final String label;
  final IconData icon;
  final bool selected;
  final VoidCallback onTap;
  const _SortButton({required this.label, required this.icon,
      required this.selected, required this.onTap});

  @override
  Widget build(BuildContext context) {
    final color = selected
        ? Theme.of(context).colorScheme.primary
        : Colors.grey.shade600;
    return GestureDetector(
      onTap: onTap,
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
        decoration: BoxDecoration(
          color: selected
              ? Theme.of(context).colorScheme.primary.withValues(alpha: 0.15)
              : Colors.transparent,
          border: Border.all(color: color.withValues(alpha: 0.5)),
          borderRadius: BorderRadius.circular(6),
        ),
        child: Row(mainAxisSize: MainAxisSize.min, children: [
          Icon(icon, size: 13, color: color),
          const SizedBox(width: 4),
          Text(label, style: TextStyle(fontSize: 12, color: color,
              fontWeight: selected ? FontWeight.w600 : FontWeight.normal)),
        ]),
      ),
    );
  }
}

class _CollapseHeader extends StatelessWidget {
  final String title;
  final int count;
  final bool expanded;
  final VoidCallback onToggle;

  const _CollapseHeader({
    required this.title, required this.count,
    required this.expanded, required this.onToggle,
  });

  @override
  Widget build(BuildContext context) {
    return InkWell(
      onTap: onToggle,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 12, 16, 4),
        child: Row(children: [
          Text(title,
              style: Theme.of(context).textTheme.titleSmall
                  ?.copyWith(fontWeight: FontWeight.bold)),
          const SizedBox(width: 8),
          Container(
            padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 2),
            decoration: BoxDecoration(
              color: Theme.of(context).colorScheme.primaryContainer,
              borderRadius: BorderRadius.circular(10),
            ),
            child: Text('$count',
                style: TextStyle(fontSize: 11, fontWeight: FontWeight.w600,
                    color: Theme.of(context).colorScheme.onPrimaryContainer)),
          ),
          const Spacer(),
          Icon(expanded ? Icons.expand_less : Icons.expand_more,
              size: 18, color: Colors.grey),
        ]),
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
              Icon(Icons.bug_report, size: 16, color: Colors.grey.shade500),
              const SizedBox(width: 8),
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
