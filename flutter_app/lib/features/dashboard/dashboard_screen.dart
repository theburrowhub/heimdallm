import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
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
import '../../shared/design_system/color_resolver.dart';
import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/severity_badge.dart';
import '../../shared/widgets/state_badge.dart';
import '../../shared/widgets/toast.dart';
import '../../shared/widgets/type_badge.dart';
import '../activity/add_pr_dialog.dart';
import '../pr_detail/review_workspace.dart';
import 'activity_filter_bar.dart';
import 'activity_filters.dart';
import 'dashboard_providers.dart';
import 'inbox_buckets.dart';
import 'inbox_sidebar.dart';
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

/// From this content width Activity shows the inbox sidebar, the compact list
/// and the preview (#910); below it, the full-width rows.
const double inboxBreakpoint = 1100;
const double inboxListWidth = 400;

String _reviewKeyOf(InstanceGroup<PR> g) =>
    reviewKeyFor(g.instanceId, g.value.repo, g.value.number);

String _emptyBucketText(InboxBucket bucket) => switch (bucket) {
  InboxBucket.needsYou => 'Nothing needs you.',
  InboxBucket.inProgress => 'Nothing in progress.',
  InboxBucket.doneToday => 'No reviews finished today yet.',
  // Only reached under the default Open filter: any other is "filters".
  InboxBucket.all => 'No open PRs.',
};

/// Repositories with their PR count under every filter but the repository
/// one, most PRs first, so picking one in the sidebar keeps the others listed.
List<(String, int)> _repoCounts(
  List<InstanceGroup<PR>> groups,
  ActivityFilters filters,
) {
  final others = filters.copyWith(repos: const {});
  final counts = <String, int>{};
  for (final g in groups) {
    if (_matchesFilters(g.value, others)) {
      counts[g.value.repo] = (counts[g.value.repo] ?? 0) + 1;
    }
  }
  return [for (final e in counts.entries) (e.key, e.value)]..sort((a, b) {
    final byCount = b.$2.compareTo(a.$2);
    return byCount != 0 ? byCount : a.$1.compareTo(b.$1);
  });
}

class _ActivityTab extends ConsumerStatefulWidget {
  const _ActivityTab();
  @override
  ConsumerState<_ActivityTab> createState() => _ActivityTabState();
}

class _ActivityTabState extends ConsumerState<_ActivityTab> {
  /// The PR the wide layout previews, by [_prIdentityKey]: the store id and
  /// the winning instance can change between refreshes, the identity cannot.
  String? _selected;

  /// Holds the wide list's keyboard focus (#913): the keys act only while
  /// this node itself is focused, so text fields and the preview's buttons
  /// keep theirs.
  final _listFocus = FocusNode(debugLabel: 'inbox-list');

  /// Row keys by [_prIdentityKey], to scroll a row picked by keyboard into
  /// view.
  final _rowKeys = <String, GlobalKey>{};

  @override
  void dispose() {
    _listFocus.dispose();
    super.dispose();
  }

  /// Selects the next ([delta] 1) or previous (-1) row of [rows], or the
  /// first/last when none is selected, and scrolls it into view.
  void _move(List<InstanceGroup<PR>> rows, int delta) {
    if (rows.isEmpty) return;
    final at = rows.indexWhere((g) => _prIdentityKey(g.value) == _selected);
    final next = at < 0
        ? (delta > 0 ? 0 : rows.length - 1)
        : (at + delta).clamp(0, rows.length - 1);
    final key = _prIdentityKey(rows[next].value);
    setState(() => _selected = key);
    WidgetsBinding.instance.addPostFrameCallback((_) {
      final row = _rowKeys[key]?.currentContext;
      if (row == null || !row.mounted) return;
      Scrollable.ensureVisible(
        row,
        duration: const Duration(milliseconds: 120),
        alignmentPolicy: delta > 0
            ? ScrollPositionAlignmentPolicy.keepVisibleAtEnd
            : ScrollPositionAlignmentPolicy.keepVisibleAtStart,
      );
    });
  }

  KeyEventResult _onListKey(
    FocusNode node,
    KeyEvent event,
    List<InstanceGroup<PR>> rows,
    InstanceGroup<PR>? selected,
  ) {
    if (!node.hasPrimaryFocus || event is KeyUpEvent) {
      return KeyEventResult.ignored;
    }
    final key = event.logicalKey;
    if (key == LogicalKeyboardKey.arrowDown) {
      _move(rows, 1);
    } else if (key == LogicalKeyboardKey.arrowUp) {
      _move(rows, -1);
    } else if (event is KeyRepeatEvent) {
      return KeyEventResult.ignored;
    } else if (key == LogicalKeyboardKey.enter && selected != null) {
      context.push(prDetailRoute(selected.value.id, selected.instanceId));
    } else if (key == LogicalKeyboardKey.escape && _selected != null) {
      setState(() => _selected = null);
    } else {
      return KeyEventResult.ignored;
    }
    return KeyEventResult.handled;
  }

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

    // Inbox buckets (#910), over the filtered rows so the counts match what
    // the list can show.
    final reviewingKeys = ref.watch(reviewingPRsProvider);
    final now = DateTime.now();
    final bucketed = [
      for (final g in filtered)
        (
          group: g,
          bucket: bucketOf(
            g.value,
            reviewing: reviewingKeys.containsKey(_reviewKeyOf(g)),
            now: now,
          ),
        ),
    ];
    final counts = {
      for (final b in InboxBucket.values)
        b: b == InboxBucket.all
            ? filtered.length
            : bucketed.where((e) => e.bucket == b).length,
    };
    final bucket = ref.watch(inboxBucketProvider);
    final visible = [
      for (final e in bucketed)
        if (bucket == InboxBucket.all || e.bucket == bucket) e.group,
    ];
    void selectBucket(InboxBucket b) =>
        ref.read(inboxBucketProvider.notifier).set(b);

    final viewMode = filters.viewMode;
    final toolbar = ActivityFilterBar(
      allRepos: allRepos,
      sort: sort,
      onSortChanged: (mode) => ref.read(reviewsSortProvider.notifier).set(mode),
      onAddPR: () => showAddPRDialog(context),
    );
    final countLine = filters.hasFilters || bucket != InboxBucket.all
        ? Padding(
            padding: const EdgeInsets.fromLTRB(16, 0, 16, 4),
            child: AppText.muted(
              '${visible.length} item${visible.length == 1 ? '' : 's'}',
            ),
          )
        : null;
    final Widget? emptyMessage = prs.isEmpty
        ? const Text('No activity yet')
        : filtered.isEmpty && filters.hasFilters
        ? const Text('No items match the current filters.')
        : visible.isEmpty
        ? Text(_emptyBucketText(bucket))
        : null;

    Widget grid() => GridView.builder(
      padding: const EdgeInsets.all(8),
      gridDelegate: AppGridDelegate.entities(),
      itemCount: visible.length,
      itemBuilder: (ctx, i) => _ActivityGridTile(pr: visible[i].value),
    );

    return LayoutBuilder(
      builder: (context, constraints) {
        if (constraints.maxWidth < inboxBreakpoint) {
          final header = <Widget>[
            toolbar,
            if (prs.isNotEmpty)
              Padding(
                padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
                child: Align(
                  alignment: Alignment.centerLeft,
                  child: AppSegmentedFilter<InboxBucket>(
                    segments: [
                      for (final b in InboxBucket.values)
                        AppSegment(value: b, label: b.label, count: counts[b]),
                    ],
                    current: bucket,
                    onChanged: selectBucket,
                  ),
                ),
              ),
            ?countLine,
          ];
          if (emptyMessage != null) {
            return Column(
              children: [
                ...header,
                Expanded(child: Center(child: emptyMessage)),
              ],
            );
          }
          if (viewMode == 'grid') {
            return Column(
              children: [
                ...header,
                Expanded(child: grid()),
              ],
            );
          }
          return ListView(
            padding: const EdgeInsets.symmetric(vertical: 8),
            children: [
              ...header,
              ...visible.map((group) => _PRTile(group: group)),
            ],
          );
        }

        // Wide: source list, compact rows and the selected PR's review.
        final selected = visible
            .where((g) => _prIdentityKey(g.value) == _selected)
            .firstOrNull;
        final Widget content;
        if (emptyMessage != null && (prs.isEmpty || filtered.isEmpty)) {
          content = Center(child: emptyMessage);
        } else if (viewMode == 'grid') {
          content = emptyMessage != null ? Center(child: emptyMessage) : grid();
        } else {
          final ids = {for (final g in visible) _prIdentityKey(g.value)};
          _rowKeys.removeWhere((id, _) => !ids.contains(id));
          content = Focus(
            focusNode: _listFocus,
            autofocus: true,
            onKeyEvent: (node, event) =>
                _onListKey(node, event, visible, selected),
            // A click anywhere in the list or the preview hands the keys back
            // to the list; a field or text that takes focus on the tap gets it
            // after this.
            child: Listener(
              onPointerDown: (_) => _listFocus.requestFocus(),
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  SizedBox(
                    width: inboxListWidth,
                    child: emptyMessage != null
                        ? Center(child: emptyMessage)
                        : ListView.builder(
                            key: const Key('inbox-list'),
                            itemCount: visible.length,
                            itemBuilder: (context, i) {
                              final g = visible[i];
                              final id = _prIdentityKey(g.value);
                              return _InboxRow(
                                key: _rowKeys.putIfAbsent(id, GlobalKey.new),
                                group: g,
                                reviewing: reviewingKeys.containsKey(
                                  _reviewKeyOf(g),
                                ),
                                selected: g == selected,
                                now: now,
                                onTap: () {
                                  _listFocus.requestFocus();
                                  setState(() => _selected = id);
                                },
                              );
                            },
                          ),
                  ),
                  const VerticalDivider(width: 1),
                  Expanded(
                    child: selected == null
                        ? _EmptyPreview(counts: counts, keys: _listFocus)
                        : _PrPreview(
                            key: ValueKey(_prIdentityKey(selected.value)),
                            group: selected,
                          ),
                  ),
                ],
              ),
            ),
          );
        }
        return Row(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            InboxSidebar(
              bucket: bucket,
              counts: counts,
              onBucket: selectBucket,
              repos: _repoCounts(prGroups, filters),
              repo: filters.repos.length == 1 ? filters.repos.first : null,
              onRepo: (name) => ref
                  .read(activityFiltersProvider.notifier)
                  .update(
                    filters.copyWith(
                      repos:
                          filters.repos.length == 1 &&
                              filters.repos.contains(name)
                          ? const {}
                          : {name},
                    ),
                  ),
            ),
            const VerticalDivider(width: 1),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  toolbar,
                  ?countLine,
                  const Divider(height: 1),
                  Expanded(child: content),
                ],
              ),
            ),
          ],
        );
      },
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

/// Review, cancel and dismiss for one PR row, shared by the full-width row and
/// the wide layout's preview.
mixin _PrActions<W extends ConsumerStatefulWidget> on ConsumerState<W> {
  /// The PR the actions act on; every instance that reported it is in
  /// [InstanceGroup.members].
  InstanceGroup<PR> get group;

  PR get _pr => group.value;
  String get _instanceId => group.instanceId;

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
    final members = group.members;
    // Undo runs after the reload has dropped the dismissed row, when this
    // widget's ref can no longer be used (#911): resolve what it needs now.
    final container = ProviderScope.containerOf(context, listen: false);
    final clients = {
      for (final m in members) m: clientForInstanceOf(ref, m.instanceId),
    };
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
            clients.entries.map((e) async {
              try {
                await e.value.undismissPR(e.key.value.id);
              } catch (_) {
                // Best-effort undo: a member that fails to un-dismiss stays
                // dismissed there, which is recoverable from the Dismissed
                // filter — better than blocking the other members' undo.
              }
            }),
          );
          container.invalidate(prsByInstanceProvider);
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
}

class _PRTileState extends ConsumerState<_PRTile> with _PrActions<_PRTile> {
  @override
  InstanceGroup<PR> get group => widget.group;

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

// ── Wide layout: compact row, preview ─────────────────────────────────────────

/// A PR in the wide layout's list: title, where it is, and what its review
/// says. Its actions live in the preview.
class _InboxRow extends StatelessWidget {
  final InstanceGroup<PR> group;
  final bool reviewing;
  final bool selected;
  final DateTime now;
  final VoidCallback onTap;

  const _InboxRow({
    super.key,
    required this.group,
    required this.reviewing,
    required this.selected,
    required this.now,
    required this.onTap,
  });

  @override
  Widget build(BuildContext context) {
    final pr = group.value;
    final theme = Theme.of(context);
    final status = prStatusOf(pr, reviewing: reviewing);
    final statusColor = resolveAppColor(context, status.color);
    final accent = resolveAppColor(context, AppColors.accent);
    final muted = resolveAppColor(context, AppColors.textMuted);
    final small = theme.textTheme.bodySmall?.copyWith(color: muted);
    final review = pr.latestReview;
    final running = reviewing || (pr.reviewStatus?.active ?? false);
    // The counts belong to the review the status describes, not to a run
    // that is going on or failed after it.
    final findings = review != null && !running && currentFailure(pr) == null
        ? findingCounts(review)
        : '';
    final row = Semantics(
      button: true,
      selected: selected,
      child: Material(
        color: selected ? accent.withValues(alpha: 0.12) : Colors.transparent,
        child: InkWell(
          onTap: onTap,
          child: Container(
            decoration: BoxDecoration(
              border: Border(
                left: BorderSide(
                  color: selected ? accent : Colors.transparent,
                  width: 3,
                ),
                bottom: BorderSide(
                  color: resolveAppColor(context, AppColors.border),
                ),
              ),
            ),
            padding: const EdgeInsets.fromLTRB(13, 11, 16, 11),
            child: Opacity(
              opacity: pr.state == 'open' ? 1 : 0.6,
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Padding(
                    padding: const EdgeInsets.only(top: 1),
                    child: running
                        ? SizedBox(
                            width: 16,
                            height: 16,
                            child: CircularProgressIndicator(
                              strokeWidth: 2,
                              color: statusColor,
                            ),
                          )
                        : Icon(status.icon, size: 16, color: statusColor),
                  ),
                  const SizedBox(width: 10),
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Row(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Expanded(
                              child: Text(
                                pr.title,
                                maxLines: 2,
                                overflow: TextOverflow.ellipsis,
                                style: theme.textTheme.bodyMedium?.copyWith(
                                  fontWeight: FontWeight.w600,
                                  height: 1.3,
                                ),
                              ),
                            ),
                            const SizedBox(width: 8),
                            Text(relativeTime(pr.updatedAt, now), style: small),
                          ],
                        ),
                        const SizedBox(height: 3),
                        Text(
                          '${pr.repo} #${pr.number} · ${pr.author}'
                          '${pr.state == 'open' ? '' : ' · ${pr.state}'}',
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: small,
                        ),
                        const SizedBox(height: 5),
                        Text.rich(
                          TextSpan(
                            children: [
                              TextSpan(
                                text: status.label,
                                style: TextStyle(
                                  color: statusColor,
                                  fontWeight: FontWeight.w600,
                                ),
                              ),
                              if (findings.isNotEmpty)
                                TextSpan(text: ' · $findings'),
                            ],
                          ),
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: small,
                        ),
                        if (group.members.isNotEmpty) ...[
                          const SizedBox(height: 5),
                          InstanceBadges(
                            instances: [
                              for (final m in group.orderedMembers)
                                (id: m.instanceId, name: m.instanceName),
                            ],
                            compact: true,
                          ),
                        ],
                      ],
                    ),
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
    return row;
  }
}

/// The selected PR's latest review beside the list, with its actions. The
/// review comes with the list payload, so this makes no request of its own.
class _PrPreview extends ConsumerStatefulWidget {
  final InstanceGroup<PR> group;
  const _PrPreview({super.key, required this.group});

  @override
  ConsumerState<_PrPreview> createState() => _PrPreviewState();
}

class _PrPreviewState extends ConsumerState<_PrPreview>
    with _PrActions<_PrPreview> {
  @override
  InstanceGroup<PR> get group => widget.group;

  @override
  Widget build(BuildContext context) {
    final pr = _pr;
    final failure = currentFailure(pr);
    final isReviewing =
        (pr.reviewStatus?.active ?? false) ||
        ref.watch(reviewingPRsProvider).containsKey(_reviewKey);
    final danger = resolveAppColor(context, AppColors.danger);
    final review = pr.latestReview;

    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(24, 10, 16, 10),
          child: Row(
            mainAxisAlignment: MainAxisAlignment.end,
            children: [
              if (isReviewing)
                OutlinedButton.icon(
                  icon: _cancelling
                      ? const SizedBox(
                          width: 12,
                          height: 12,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        )
                      : const Icon(Icons.stop_circle_outlined, size: 16),
                  label: Text(_cancelling ? 'Cancelling…' : 'Cancel review'),
                  onPressed: _cancelling ? null : _cancelReview,
                )
              else
                OutlinedButton.icon(
                  icon: const Icon(Icons.refresh, size: 16),
                  label: Text(
                    failure != null
                        ? 'Retry'
                        : review == null
                        ? 'Review'
                        : 'Re-review',
                  ),
                  onPressed: pr.repo.isEmpty ? null : _triggerReview,
                ),
              const SizedBox(width: 8),
              IconButton(
                icon: const Icon(Icons.close, size: 18),
                tooltip: 'Dismiss PR',
                onPressed: _dismiss,
              ),
              const SizedBox(width: 4),
              FilledButton.icon(
                key: const Key('inbox-preview-open'),
                icon: const Icon(Icons.open_in_full, size: 16),
                label: const Text('Open'),
                onPressed: () =>
                    context.push(prDetailRoute(pr.id, _instanceId)),
              ),
            ],
          ),
        ),
        if (failure != null && !isReviewing)
          Container(
            key: const Key('inbox-preview-failure'),
            color: danger.withValues(alpha: 0.10),
            padding: const EdgeInsets.fromLTRB(24, 10, 24, 10),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Icon(Icons.error_outline, size: 16, color: danger),
                const SizedBox(width: 8),
                Expanded(
                  child: Tooltip(
                    message: failure.error,
                    child: Text(
                      reviewFailureSummary(failure),
                      maxLines: 4,
                      overflow: TextOverflow.ellipsis,
                      style: Theme.of(
                        context,
                      ).textTheme.bodySmall?.copyWith(color: danger),
                    ),
                  ),
                ),
              ],
            ),
          ),
        const Divider(height: 1),
        Expanded(
          child: ReviewWorkspace(
            pr: pr,
            reviews: [?review],
            // The toolbar above starts reviews; the workspace's own button is
            // for a PR never reviewed, and is off while one runs.
            onReview: isReviewing || pr.repo.isEmpty ? null : _triggerReview,
          ),
        ),
      ],
    );
  }
}

/// The preview before a row is picked: what the buckets hold.
class _EmptyPreview extends StatelessWidget {
  final Map<InboxBucket, int> counts;

  /// The list's focus: the keyboard hint shows only while the keys work.
  final FocusNode keys;
  const _EmptyPreview({required this.counts, required this.keys});

  @override
  Widget build(BuildContext context) {
    final muted = resolveAppColor(context, AppColors.textMuted);
    final theme = Theme.of(context);
    return Center(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.center,
        children: [
          Icon(Icons.inbox_outlined, size: 44, color: muted),
          const SizedBox(height: 14),
          Text(
            'Select a PR to read its review',
            style: theme.textTheme.titleMedium,
          ),
          const SizedBox(height: 6),
          Text(
            '${counts[InboxBucket.needsYou] ?? 0} need you · '
            '${counts[InboxBucket.inProgress] ?? 0} in progress · '
            '${counts[InboxBucket.doneToday] ?? 0} done today',
            style: theme.textTheme.bodyMedium?.copyWith(color: muted),
          ),
          const SizedBox(height: 18),
          // Icons, not ↑↓ glyphs: not every bundled font has the arrows. Inline
          // spans, so the hint wraps in a narrow preview.
          ListenableBuilder(
            listenable: keys,
            builder: (context, _) => keys.hasPrimaryFocus
                ? Text.rich(
                    TextSpan(
                      children: [
                        for (final icon in const [
                          Icons.arrow_upward,
                          Icons.arrow_downward,
                        ])
                          WidgetSpan(
                            alignment: PlaceholderAlignment.middle,
                            child: Icon(icon, size: 13, color: muted),
                          ),
                        const TextSpan(
                          text: ' to move · Enter to open · Esc to clear',
                        ),
                      ],
                    ),
                    textAlign: TextAlign.center,
                    style: theme.textTheme.bodySmall?.copyWith(color: muted),
                  )
                : const SizedBox.shrink(),
          ),
        ],
      ),
    );
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
