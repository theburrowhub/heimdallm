import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:shared_preferences/shared_preferences.dart';
import '../../core/api/api_client.dart';
import '../../core/instances/aggregation.dart';
import '../../core/instances/instances_providers.dart';
import '../../core/instances/models.dart' show RoutingRules;
import '../instances/widgets/instance_badge.dart';
import '../../core/models/pr.dart';
import '../../core/models/review_status.dart';
import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/severity_badge.dart';
import '../../shared/widgets/state_badge.dart';
import '../../shared/widgets/toast.dart';
import '../../shared/widgets/type_badge.dart';
import '../activity/add_pr_dialog.dart';
import 'activity_filter_bar.dart';
import 'activity_filters.dart';
import 'dashboard_providers.dart';
import '../server/server_actions.dart' as server_actions;

class DashboardScreen extends ConsumerWidget {
  const DashboardScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    // A bare Scaffold (no AppBar) guarantees a Material ancestor for this
    // screen's TextFields/buttons whether it's hosted inside the app shell
    // (which has its own outer Scaffold) or pumped standalone in tests.
    return const Scaffold(body: _ActivityTab());
  }
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

/// The identity key [groupByIdentity] collapses PR rows on: repo + number,
/// not the local store id. A PR's `id` is a per-daemon SQLite row and is not
/// comparable across instances (two daemons can independently assign PR #769
/// different ids); `githubId` is not reliable either — the daemon's own
/// GetPRByRepoNumber falls back away from it because GitHub's Search and
/// Pulls APIs can return different global ids for the same PR. repo+number is
/// what `reviewKeyFor` and `reconcileReviewing` already key on for the same
/// reason.
String _prIdentityKey(PR pr) => '${pr.repo}#${pr.number}';

/// Picks which instance's row is authoritative for a group of the same PR
/// reported by several instances (theburrowhub/heimdallm#769).
///
/// In order: the routing owner, if routing is engaged and one of the
/// candidates is it; otherwise whichever candidate actually has a review
/// recorded (ties broken by the newer review) — a row with no work yet is
/// less informative than one that does, regardless of registration order;
/// otherwise the first candidate, deterministically.
///
/// [rules]'s `enabled` is checked explicitly rather than trusting
/// `ownerFor` alone: the daemon's own `Router.OwnerFor` returns "" when
/// routing is disabled, but `RoutingRules.ownerFor` does not re-derive that —
/// it happily returns `defaultInstance` even with routing off, which would
/// make a disabled fallback win over a real review.
InstanceScoped<T> _preferOwner<T>(
  List<InstanceScoped<T>> candidates, {
  required String Function(T value) repoOf,
  required bool Function(T value) hasWork,
  required int Function(T value) reviewIdOf,
  RoutingRules? rules,
}) {
  if (candidates.length == 1) return candidates.first;
  if (rules != null && rules.enabled) {
    final owner = rules.ownerFor(repoOf(candidates.first.value));
    if (owner.isNotEmpty) {
      final match = candidates.where((c) => c.instanceId == owner).firstOrNull;
      if (match != null) return match;
    }
  }
  final withWork = candidates.where((c) => hasWork(c.value)).toList();
  if (withWork.isEmpty) return candidates.first;
  withWork.sort((a, b) => reviewIdOf(b.value).compareTo(reviewIdOf(a.value)));
  return withWork.first;
}

int _priorityKey(PR pr) => pr.latestReview == null
    ? 0
    : switch (pr.latestReview!.severity.toLowerCase()) {
        'high' => 1,
        'medium' => 2,
        _ => 3,
      };

bool _matchesFilters(PR pr, ActivityFilters filters) {
  // Org filter
  if (filters.orgs.isNotEmpty) {
    final org = pr.repo.contains('/') ? pr.repo.split('/').first : pr.repo;
    if (!filters.orgs.contains(org)) return false;
  }
  // Repo filter
  if (filters.repos.isNotEmpty) {
    if (!filters.repos.contains(pr.repo)) return false;
  }
  // State filter
  if (filters.states.isNotEmpty) {
    if (!filters.states.contains(pr.state)) return false;
  }
  // Search
  if (filters.search.isNotEmpty) {
    final q = filters.search.toLowerCase();
    if (!pr.title.toLowerCase().contains(q) &&
        !pr.repo.toLowerCase().contains(q) &&
        !pr.number.toString().contains(q) &&
        !pr.author.toLowerCase().contains(q)) {
      return false;
    }
  }
  return true;
}

void _sortItems(List<InstanceGroup<PR>> items, SortMode mode) {
  switch (mode) {
    case SortMode.priority:
      items.sort((a, b) {
        final sev = _priorityKey(a.value).compareTo(_priorityKey(b.value));
        if (sev != 0) return sev;
        return b.value.updatedAt.compareTo(a.value.updatedAt);
      });
    case SortMode.newest:
      items.sort((a, b) => b.value.updatedAt.compareTo(a.value.updatedAt));
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
        if (event.type == 'pr_state_changed') {
          ref.invalidate(prsByInstanceProvider);
        }
      });
    });

    // Watch the aggregating providers so every row knows which instance
    // served it; the flat lists below are just their values.
    final prsAsync = ref.watch(prsByInstanceProvider);
    // Discovery is global (every instance learns about every repo), so the
    // same PR legitimately arrives from more than one instance's
    // fan-out — grouping below collapses those into one row instead of the
    // duplicate rows behind theburrowhub/heimdallm#769. `.value` rather than
    // `.future`/`.when`: the activity list must not block or spinner waiting
    // on the control plane, and a standalone install's kNotAClusterHubError
    // just leaves this null, which _preferOwner already treats the same as
    // "no rules".
    final routingRules = ref.watch(routingRulesProvider).value;
    final sort = ref.watch(reviewsSortProvider);
    final filters = ref.watch(activityFiltersProvider);
    final emptyToolbar = ActivityFilterBar(
      allRepos: const {},
      sort: sort,
      onSortChanged: (mode) => ref.read(reviewsSortProvider.notifier).set(mode),
      onAddPR: () => showAddPRDialog(context),
    );

    // Spinner/error only before the first value: a refresh (e.g. after a
    // dismiss) must keep the rows mounted so their toasts' Undo still works.
    if (prsAsync.isLoading && !prsAsync.hasValue) {
      return Column(
        children: [
          emptyToolbar,
          const Expanded(child: Center(child: CircularProgressIndicator())),
        ],
      );
    }
    if (prsAsync.hasError && !prsAsync.hasValue) {
      return Column(
        children: [
          emptyToolbar,
          Expanded(child: _errorView(context, prsAsync.error!)),
        ],
      );
    }

    final prScoped = prsAsync.value?.items ?? const <InstanceScoped<PR>>[];
    final prs = prScoped.map((e) => e.value).toList();

    // Collect all known repos for the filter bar.
    final allRepos = <String>{...prs.map((p) => p.repo)}..remove('');

    // Group before building items, not after: the "N items" count and the
    // filters below must see the collapsed rows, or a PR reported by two
    // instances would count (and could be found by search) twice.
    final prGroups = groupByIdentity<PR>(
      prScoped.where((e) => e.value.repo.isNotEmpty).toList(),
      keyOf: _prIdentityKey,
      pick: (candidates) => _preferOwner<PR>(
        candidates,
        repoOf: (pr) => pr.repo,
        hasWork: (pr) => pr.latestReview != null,
        reviewIdOf: (pr) => pr.latestReview?.id ?? 0,
        rules: routingRules,
      ),
    );
    // Apply filters.
    final filtered = prGroups
        .where((group) => _matchesFilters(group.value, filters))
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
          child: AppText.muted(
            '${filtered.length} item${filtered.length == 1 ? '' : 's'}',
          ),
        ),
    ];

    if (prs.isEmpty) {
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
              gridDelegate: AppGridDelegate.entities(),
              itemCount: filtered.length,
              itemBuilder: (ctx, i) => _ActivityGridTile(pr: filtered[i].value),
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
        ...filtered.map((group) => _PRTile(group: group)),
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
                onPressed: () => ref.invalidate(prsByInstanceProvider),
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
                    : () => server_actions.startDaemon(context, ref),
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
  final InstanceGroup<PR> group;
  const _PRTile({required this.group});

  @override
  ConsumerState<_PRTile> createState() => _PRTileState();
}

class _PRTileState extends ConsumerState<_PRTile> {
  PR get _pr => widget.group.value;
  String get _instanceId => widget.group.instanceId;

  String get _reviewKey => reviewKeyFor(_instanceId, _pr.repo, _pr.number);

  /// The client for the instance holding this PR, not whichever one the
  /// dashboard is currently scoped to.
  ApiClient get _api => clientForInstanceOf(ref, _instanceId);
  bool _cancelling = false;

  Future<void> _triggerReview() async {
    // Optimistically mark as reviewing before the SSE event arrives.
    // Baseline = current latestReview.id (0 if none) so reconciliation can
    // later distinguish a stuck key from an in-progress re-review.
    final baseline = _pr.latestReview?.id ?? 0;
    ref
        .read(reviewingPRsProvider.notifier)
        .update((s) => {...s, _reviewKey: baseline});
    try {
      await _api.triggerReview(_pr.id);
    } catch (e) {
      ref
          .read(reviewingPRsProvider.notifier)
          .update((s) => Map.of(s)..remove(_reviewKey));
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  /// Dismisses the PR on EVERY instance that reported it, not just the
  /// primary. A group exists because discovery is global — every member
  /// still has its own local row for this PR — so dismissing only the
  /// primary would leave the others' rows live, and the PR would reappear
  /// (now with one fewer badge) on the next refresh.
  Future<void> _dismiss() async {
    final members = widget.group.members;
    final errors = <Object>[];
    await Future.wait(
      members.map((m) async {
        try {
          await clientForInstanceOf(ref, m.instanceId).dismissPR(m.value.id);
        } catch (e) {
          errors.add(e);
        }
      }),
    );
    ref.invalidate(prsByInstanceProvider);
    if (!mounted) return;
    if (errors.isEmpty) {
      showToast(
        context,
        'PR #${_pr.number} dismissed',
        duration: const Duration(seconds: 5),
        actionLabel: 'Undo',
        onAction: () async {
          await Future.wait(
            members.map((m) async {
              try {
                await clientForInstanceOf(
                  ref,
                  m.instanceId,
                ).undismissPR(m.value.id);
              } catch (_) {
                // Best-effort undo: a member that fails to un-dismiss stays
                // dismissed there, which is recoverable from the Dismissed
                // filter — better than blocking the other members' undo.
              }
            }),
          );
          ref.invalidate(prsByInstanceProvider);
        },
      );
    } else {
      showToast(
        context,
        'Error dismissing PR #${_pr.number}: ${errors.first}',
        isError: true,
      );
    }
  }

  Future<void> _cancelReview() async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (dialogContext) => AlertDialog(
        title: const Text('Cancel this review?'),
        content: Text(
          'The active agent process for ${_pr.repo} #${_pr.number} '
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
      await _api.cancelReview(_pr.id);
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
    final pr = _pr;
    final reviewed = pr.latestReview != null;
    final status = pr.reviewStatus;
    final failure = status != null && !status.active && status.error.isNotEmpty
        ? status
        : null;
    final isReviewing =
        (status?.active ?? false) ||
        ref.watch(reviewingPRsProvider).containsKey(_reviewKey);

    return AppListRow(
      dimmed: pr.state != 'open',
      onTap: () => context.push(prDetailRoute(pr.id, _instanceId)),
      accentColor: isReviewing
          ? Theme.of(context).colorScheme.primary
          : failure != null
          ? Theme.of(context).colorScheme.error
          : reviewed
          ? _severityColor(pr.latestReview!.severity)
          : Colors.grey.shade600,
      accentHeight: failure == null ? 48 : 62,
      leading: [
        const TypeBadge(type: 'pr'),
        StateBadge(state: pr.state),
      ],
      title: Text(
        pr.title,
        style: const TextStyle(fontWeight: FontWeight.w600),
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
      ),
      subtitle: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisSize: MainAxisSize.min,
        children: [
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
              if (widget.group.members.isNotEmpty) ...[
                const SizedBox(width: 6),
                Flexible(
                  child: InstanceBadges(
                    instances: [
                      for (final m in widget.group.orderedMembers)
                        (id: m.instanceId, name: m.instanceName),
                    ],
                    compact: true,
                  ),
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
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                  color: Theme.of(context).colorScheme.error,
                ),
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
              ),
            ),
          ],
        ],
      ),
      trailing: [
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
        if (isReviewing)
          SizedBox(
            height: 28,
            child: OutlinedButton.icon(
              icon: _cancelling
                  ? const SizedBox(
                      width: 12,
                      height: 12,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    )
                  : const Icon(Icons.stop_circle_outlined, size: 15),
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

// ── Grid tile ─────────────────────────────────────────────────────────────────

class _ActivityGridTile extends StatelessWidget {
  final PR pr;
  const _ActivityGridTile({required this.pr});

  @override
  Widget build(BuildContext context) {
    final severity = pr.latestReview?.severity;
    return AppGridCard(
      dimmed: pr.state != 'open',
      header: Row(
        children: [
          Container(
            padding: const EdgeInsets.symmetric(horizontal: 5, vertical: 1),
            decoration: BoxDecoration(
              color: AppColors.featurePrReview.resolve(context),
              borderRadius: BorderRadius.circular(3),
            ),
            child: const Text(
              'PR',
              style: TextStyle(
                color: Colors.white,
                fontSize: 9,
                fontWeight: FontWeight.bold,
              ),
            ),
          ),
          const Spacer(),
          StateBadge(state: pr.state),
        ],
      ),
      title: Text(
        pr.title,
        maxLines: 2,
        overflow: TextOverflow.ellipsis,
        style: const TextStyle(fontSize: 12, fontWeight: FontWeight.w500),
      ),
      subtitle: AppText.muted(
        '${pr.repo} #${pr.number} · ${pr.author}',
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
      ),
      footer: Row(
        children: [
          if (severity != null) SeverityBadge(severity: severity),
          const Spacer(),
          AppText.muted(_timeAgo(pr.updatedAt)),
        ],
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
