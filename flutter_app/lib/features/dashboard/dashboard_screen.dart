import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../../core/models/pr.dart';
import '../../shared/widgets/severity_badge.dart';
import '../../shared/widgets/toast.dart';
import '../agents/agents_screen.dart';
import '../repositories/repos_screen.dart';
import '../stats/stats_screen.dart';
import 'dashboard_providers.dart';

class DashboardScreen extends ConsumerWidget {
  const DashboardScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    return DefaultTabController(
      length: 4,
      child: Scaffold(
        appBar: AppBar(
          title: const Text('Heimdallr'),
          actions: [
            IconButton(
              icon: const Icon(Icons.settings),
              onPressed: () => context.push('/config'),
            ),
            IconButton(
              icon: const Icon(Icons.refresh),
              onPressed: () {
                ref.invalidate(prsProvider);
                ref.invalidate(statsProvider);
              },
            ),
          ],
          bottom: const TabBar(
            tabs: [
              Tab(icon: Icon(Icons.rate_review), text: 'Reviews'),
              Tab(icon: Icon(Icons.folder_outlined), text: 'Repositories'),
              Tab(icon: Icon(Icons.auto_awesome), text: 'Prompts'),
              Tab(icon: Icon(Icons.bar_chart), text: 'Stats'),
            ],
          ),
        ),
        body: const TabBarView(
          children: [
            _ReviewsTab(),
            ReposScreen(),
            AgentsScreen(),
            StatsScreen(),
          ],
        ),
      ),
    );
  }
}

// ── Reviews tab ──────────────────────────────────────────────────────────────

/// Sort order within a PR section: pending → has issues → resolved
int _prSortKey(PR p) {
  if (p.latestReview == null) return 0;
  final s = p.latestReview!.severity.toLowerCase();
  if (s == 'high' || s == 'medium') return 1;
  return 2;
}

List<PR> _sortedPRs(List<PR> prs) =>
    [...prs]..sort((a, b) => _prSortKey(a).compareTo(_prSortKey(b)));

class _ReviewsTab extends ConsumerStatefulWidget {
  const _ReviewsTab();
  @override
  ConsumerState<_ReviewsTab> createState() => _ReviewsTabState();
}

class _ReviewsTabState extends ConsumerState<_ReviewsTab> {
  bool _reviewsExpanded = true;
  bool _prsExpanded     = true;

  @override
  Widget build(BuildContext context) {
    final prsAsync = ref.watch(prsProvider);
    final meAsync  = ref.watch(meProvider);

    return prsAsync.when(
      loading: () => const Center(child: CircularProgressIndicator()),
      error: (e, _) => _errorView(context, e),
      data: (prs) {
        final me = meAsync.valueOrNull ?? '';
        final myReviews = _sortedPRs(
            prs.where((p) => p.author.toLowerCase() != me.toLowerCase()).toList());
        final myPRs = _sortedPRs(
            prs.where((p) => p.author.toLowerCase() == me.toLowerCase()).toList());

        if (prs.isEmpty) {
          return const Center(child: Text('No open PRs found'));
        }

        return ListView(
          padding: const EdgeInsets.symmetric(vertical: 8),
          children: [
            if (myReviews.isNotEmpty) ...[
              _CollapseHeader(
                title: 'My Reviews',
                count: myReviews.length,
                expanded: _reviewsExpanded,
                onToggle: () => setState(() => _reviewsExpanded = !_reviewsExpanded),
              ),
              if (_reviewsExpanded)
                ...myReviews.map((pr) => _PRTile(pr: pr, ref: ref)),
            ],
            if (myPRs.isNotEmpty) ...[
              _CollapseHeader(
                title: 'My PRs',
                count: myPRs.length,
                expanded: _prsExpanded,
                onToggle: () => setState(() => _prsExpanded = !_prsExpanded),
              ),
              if (_prsExpanded)
                ...myPRs.map((pr) => _PRTile(pr: pr, ref: ref)),
            ],
          ],
        );
      },
    );
  }

  Widget _errorView(BuildContext context, Object e) {
    return Center(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          const Icon(Icons.wifi_off, size: 48, color: Colors.grey),
          const SizedBox(height: 12),
          const Text('Could not reach the Heimdallr daemon.',
              style: TextStyle(fontWeight: FontWeight.w600)),
          const SizedBox(height: 4),
          const Text('Go to Settings to configure and start it.',
              style: TextStyle(color: Colors.grey)),
          const SizedBox(height: 16),
          Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              TextButton(
                  onPressed: () => ref.invalidate(prsProvider),
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

class _PRTile extends StatelessWidget {
  final PR pr;
  final WidgetRef ref;
  const _PRTile({required this.pr, required this.ref});

  @override
  Widget build(BuildContext context) {
    final reviewed = pr.latestReview != null;

    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 3),
      child: InkWell(
        borderRadius: BorderRadius.circular(12),
        onTap: () => context.push('/prs/${pr.id}'),
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
          child: Row(
            children: [
              Container(
                width: 4, height: 48,
                margin: const EdgeInsets.only(right: 12),
                decoration: BoxDecoration(
                  color: reviewed
                      ? _severityColor(pr.latestReview!.severity)
                      : Colors.grey.shade600,
                  borderRadius: BorderRadius.circular(2),
                ),
              ),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(pr.title,
                        style: const TextStyle(fontWeight: FontWeight.w600),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis),
                    const SizedBox(height: 4),
                    Text('${pr.repo} · #${pr.number} · ${pr.author}',
                        style: Theme.of(context).textTheme.bodySmall,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis),
                  ],
                ),
              ),
              const SizedBox(width: 12),
              Column(
                crossAxisAlignment: CrossAxisAlignment.end,
                children: [
                  if (reviewed)
                    SeverityBadge(severity: pr.latestReview!.severity)
                  else
                    _chip('PENDING', Colors.grey.shade700),
                  const SizedBox(height: 6),
                  SizedBox(
                    height: 28,
                    child: ElevatedButton(
                      style: ElevatedButton.styleFrom(
                          padding: const EdgeInsets.symmetric(horizontal: 10),
                          textStyle: const TextStyle(fontSize: 12)),
                      onPressed: () async {
                        final api = ref.read(apiClientProvider);
                        await api.triggerReview(pr.id);
                      },
                      child: const Text('Review'),
                    ),
                  ),
                ],
              ),
              const SizedBox(width: 4),
              IconButton(
                icon: const Icon(Icons.close, size: 16),
                tooltip: 'Dismiss PR',
                color: Colors.grey.shade600,
                visualDensity: VisualDensity.compact,
                onPressed: () => _dismiss(context),
              ),
            ],
          ),
        ),
      ),
    );
  }

  Future<void> _dismiss(BuildContext context) async {
    final api = ref.read(apiClientProvider);
    try {
      await api.dismissPR(pr.id);
      ref.invalidate(prsProvider);
      if (context.mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(
            duration: const Duration(seconds: 5),
            showCloseIcon: true,
            content: Text('PR #${pr.number} dismissed'),
            action: SnackBarAction(
              label: 'Undo',
              onPressed: () async {
                await api.undismissPR(pr.id);
                ref.invalidate(prsProvider);
              },
            ),
          ),
        );
      }
    } catch (e) {
      if (context.mounted) showToast(context, 'Error: $e', isError: true);
    }
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
