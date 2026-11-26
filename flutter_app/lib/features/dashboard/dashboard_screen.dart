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
import '../../core/models/review.dart';
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
import '../merge_tracking/merge_tracking_providers.dart';
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

/// From this content width, in Board view, Activity shows the board (#920);
/// below it, and in List or Grid view, the full-width rows or cards.
const double boardBreakpoint = 960;

/// Lanes stop growing here, so a very wide window does not stretch the cards.
const double boardLaneMaxWidth = 520;

String _reviewKeyOf(InstanceGroup<PR> g) =>
    reviewKeyFor(g.instanceId, g.value.repo, g.value.number);

String _emptyBucketText(InboxBucket bucket) => switch (bucket) {
  InboxBucket.needsYou => 'Nothing needs you.',
  InboxBucket.inProgress => 'Nothing in progress.',
  InboxBucket.doneToday => 'No reviews finished today yet.',
  // Only reached under the default Open filter: any other is "filters".
  InboxBucket.all => 'No open PRs.',
};

String _emptyLaneText(BoardLane lane) => switch (lane) {
  BoardLane.needsYou => 'Nothing needs you.',
  BoardLane.inProgress => 'Nothing in progress.',
  BoardLane.reviewed => 'No reviews yet.',
};

class _ActivityTab extends ConsumerStatefulWidget {
  const _ActivityTab();
  @override
  ConsumerState<_ActivityTab> createState() => _ActivityTabState();
}

class _ActivityTabState extends ConsumerState<_ActivityTab> {
  /// The card the board's keyboard selection is on, by [_prIdentityKey]: the
  /// store id and the winning instance can change between refreshes, the
  /// identity cannot.
  String? _selected;

  /// Holds the board's keyboard focus (#913, #920): the keys act only while
  /// this node itself is focused, so text fields and the cards' buttons keep
  /// theirs.
  final _boardFocus = FocusNode(debugLabel: 'activity-board');

  /// Card keys by [_prIdentityKey], to scroll a card picked by keyboard into
  /// view.
  final _cardKeys = <String, GlobalKey>{};

  @override
  void dispose() {
    _boardFocus.dispose();
    super.dispose();
  }

  void _select(String id, {required bool forward}) {
    setState(() => _selected = id);
    WidgetsBinding.instance.addPostFrameCallback((_) {
      final card = _cardKeys[id]?.currentContext;
      if (card == null || !card.mounted) return;
      Scrollable.ensureVisible(
        card,
        duration: const Duration(milliseconds: 120),
        alignmentPolicy: forward
            ? ScrollPositionAlignmentPolicy.keepVisibleAtEnd
            : ScrollPositionAlignmentPolicy.keepVisibleAtStart,
      );
    });
  }

  /// Moves the selection [delta] cards within its lane (↑/↓) or [delta] lanes
  /// sideways at the same position (←/→, skipping empty lanes). With nothing
  /// selected, any arrow picks the first card on the board.
  void _move(
    List<List<InstanceGroup<PR>>> lanes, {
    int down = 0,
    int across = 0,
  }) {
    var lane = -1, index = 0;
    for (var l = 0; l < lanes.length && lane < 0; l++) {
      final i = lanes[l].indexWhere(
        (g) => _prIdentityKey(g.value) == _selected,
      );
      if (i >= 0) (lane, index) = (l, i);
    }
    if (lane < 0) {
      final first = lanes.where((l) => l.isNotEmpty).firstOrNull;
      if (first != null) {
        _select(_prIdentityKey(first.first.value), forward: true);
      }
      return;
    }
    if (down != 0) {
      final cards = lanes[lane];
      final next = (index + down).clamp(0, cards.length - 1);
      _select(_prIdentityKey(cards[next].value), forward: down > 0);
      return;
    }
    for (var l = lane + across; l >= 0 && l < lanes.length; l += across) {
      if (lanes[l].isEmpty) continue;
      final cards = lanes[l];
      _select(
        _prIdentityKey(cards[index.clamp(0, cards.length - 1)].value),
        forward: true,
      );
      return;
    }
  }

  KeyEventResult _onBoardKey(
    FocusNode node,
    KeyEvent event,
    List<List<InstanceGroup<PR>>> lanes,
    InstanceGroup<PR>? selected,
  ) {
    if (!node.hasPrimaryFocus || event is KeyUpEvent) {
      return KeyEventResult.ignored;
    }
    final key = event.logicalKey;
    if (key == LogicalKeyboardKey.arrowDown) {
      _move(lanes, down: 1);
    } else if (key == LogicalKeyboardKey.arrowUp) {
      _move(lanes, down: -1);
    } else if (key == LogicalKeyboardKey.arrowRight) {
      _move(lanes, across: 1);
    } else if (key == LogicalKeyboardKey.arrowLeft) {
      _move(lanes, across: -1);
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
    Widget toolbarFor(double width) => ActivityFilterBar(
      allRepos: allRepos,
      sort: sort,
      onSortChanged: (mode) => ref.read(reviewsSortProvider.notifier).set(mode),
      onAddPR: () => showAddPRDialog(context),
      boardFits: width >= boardBreakpoint,
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
        if (viewMode != 'board' || constraints.maxWidth < boardBreakpoint) {
          final header = <Widget>[
            toolbarFor(constraints.maxWidth),
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

        // Wide, Board view: the lanes (#920).
        final boardCount = filters.hasFilters
            ? Padding(
                padding: const EdgeInsets.fromLTRB(16, 0, 16, 4),
                child: AppText.muted(
                  '${filtered.length} item${filtered.length == 1 ? '' : 's'}',
                ),
              )
            : null;
        if (emptyMessage != null && (prs.isEmpty || filtered.isEmpty)) {
          return Column(
            children: [
              toolbarFor(constraints.maxWidth),
              ?boardCount,
              Expanded(child: Center(child: emptyMessage)),
            ],
          );
        }
        final byLane = {
          for (final l in BoardLane.values) l: <InstanceGroup<PR>>[],
        };
        final todayIds = <String>{};
        for (final e in bucketed) {
          final reviewing = reviewingKeys.containsKey(_reviewKeyOf(e.group));
          byLane[laneOf(e.group.value, reviewing: reviewing, now: now)]!.add(
            e.group,
          );
          if (e.bucket == InboxBucket.doneToday) {
            todayIds.add(_prIdentityKey(e.group.value));
          }
        }
        // Reviewed: today's reviews first, then the earlier ones, each in the
        // toolbar's sort order.
        final reviewed = byLane[BoardLane.reviewed]!;
        final today = [
          for (final g in reviewed)
            if (todayIds.contains(_prIdentityKey(g.value))) g,
        ];
        final earlier = [
          for (final g in reviewed)
            if (!todayIds.contains(_prIdentityKey(g.value))) g,
        ];
        final lanes = [
          byLane[BoardLane.needsYou]!,
          byLane[BoardLane.inProgress]!,
          [...today, ...earlier],
        ];
        final ids = {for (final g in filtered) _prIdentityKey(g.value)};
        _cardKeys.removeWhere((id, _) => !ids.contains(id));
        final selected = filtered
            .where((g) => _prIdentityKey(g.value) == _selected)
            .firstOrNull;
        final laneWidth = ((constraints.maxWidth - 32 - 24) / 3).clamp(
          0.0,
          boardLaneMaxWidth,
        );
        Widget lane(int i, BoardLane l) => SizedBox(
          width: laneWidth,
          child: _Lane(
            lane: l,
            count: lanes[i].length,
            sections:
                l == BoardLane.reviewed &&
                    today.isNotEmpty &&
                    earlier.isNotEmpty
                ? [('Today', today), ('Earlier', earlier)]
                : [(null, lanes[i])],
            selectedId: _selected,
            cardKeys: _cardKeys,
          ),
        );
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            toolbarFor(constraints.maxWidth),
            ?boardCount,
            Expanded(
              child: Focus(
                focusNode: _boardFocus,
                autofocus: true,
                onKeyEvent: (node, event) =>
                    _onBoardKey(node, event, lanes, selected),
                // A click anywhere on the board hands the keys back to it; a
                // field or button that takes focus on the tap gets it after.
                child: Listener(
                  onPointerDown: (_) => _boardFocus.requestFocus(),
                  child: Padding(
                    padding: const EdgeInsets.fromLTRB(16, 4, 16, 16),
                    child: Row(
                      key: const Key('activity-board'),
                      // Capped lanes leave room on a very wide window; centre
                      // them rather than pile them on the left.
                      mainAxisAlignment: MainAxisAlignment.center,
                      crossAxisAlignment: CrossAxisAlignment.stretch,
                      children: [
                        for (final (i, l) in BoardLane.values.indexed) ...[
                          if (i > 0) const SizedBox(width: 12),
                          lane(i, l),
                        ],
                      ],
                    ),
                  ),
                ),
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

// ── Board ─────────────────────────────────────────────────────────────────────

/// One board lane (#920): its name and count, then its cards in sections
/// (Reviewed splits Today from Earlier), scrolling on its own.
class _Lane extends ConsumerWidget {
  final BoardLane lane;
  final int count;
  final List<(String?, List<InstanceGroup<PR>>)> sections;
  final String? selectedId;
  final Map<String, GlobalKey> cardKeys;

  const _Lane({
    required this.lane,
    required this.count,
    required this.sections,
    required this.selectedId,
    required this.cardKeys,
  });

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final theme = Theme.of(context);
    final muted = resolveAppColor(context, AppColors.textMuted);
    final dot = resolveAppColor(context, switch (lane) {
      BoardLane.needsYou => AppColors.danger,
      BoardLane.inProgress => AppColors.accent,
      BoardLane.reviewed => AppColors.success,
    });
    final yours = lane == BoardLane.needsYou
        ? ref.watch(myPrsSummaryProvider)
        : null;
    final children = <Widget>[
      if (yours != null && !yours.isEmpty) ...[
        const _YourPrsCard(),
        const SizedBox(height: 8),
      ],
      for (final (title, cards) in sections) ...[
        if (title != null)
          Padding(
            padding: const EdgeInsets.fromLTRB(4, 6, 4, 8),
            child: Text(
              title.toUpperCase(),
              style: theme.textTheme.labelSmall?.copyWith(
                color: muted,
                fontWeight: FontWeight.w600,
                letterSpacing: 0.6,
              ),
            ),
          ),
        for (final g in cards)
          Padding(
            padding: const EdgeInsets.only(bottom: 8),
            child: _BoardCard(
              key: cardKeys.putIfAbsent(_prIdentityKey(g.value), GlobalKey.new),
              group: g,
              selected: _prIdentityKey(g.value) == selectedId,
            ),
          ),
      ],
      if (count == 0)
        Padding(
          padding: const EdgeInsets.symmetric(vertical: 24),
          child: Center(
            child: Text(
              _emptyLaneText(lane),
              style: theme.textTheme.bodyMedium?.copyWith(color: muted),
            ),
          ),
        ),
    ];
    return DecoratedBox(
      decoration: BoxDecoration(
        color: theme.colorScheme.surfaceContainerLow,
        borderRadius: BorderRadius.circular(12),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 14, 16, 10),
            child: Row(
              children: [
                Container(
                  width: 8,
                  height: 8,
                  decoration: BoxDecoration(color: dot, shape: BoxShape.circle),
                ),
                const SizedBox(width: 8),
                Text(
                  lane.label,
                  style: theme.textTheme.titleSmall?.copyWith(
                    fontWeight: FontWeight.w600,
                  ),
                ),
                const SizedBox(width: 8),
                Text(
                  '$count',
                  style: theme.textTheme.bodyMedium?.copyWith(color: muted),
                ),
              ],
            ),
          ),
          Expanded(
            child: ListView(
              key: Key('board-lane-${lane.name}'),
              padding: const EdgeInsets.fromLTRB(10, 0, 10, 4),
              children: children,
            ),
          ),
        ],
      ),
    );
  }
}

/// The operator's own PRs that need them (#915), on top of Needs you: the
/// count of the My PRs badge, opening My PRs.
class _YourPrsCard extends ConsumerWidget {
  const _YourPrsCard();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final summary = ref.watch(myPrsSummaryProvider);
    final theme = Theme.of(context);
    final accent = resolveAppColor(context, AppColors.accent);
    final muted = resolveAppColor(context, AppColors.textMuted);
    return Material(
      color: resolveAppColor(context, AppColors.surfaceRaised),
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(10),
        side: BorderSide(color: resolveAppColor(context, AppColors.border)),
      ),
      child: InkWell(
        key: const Key('board-your-prs'),
        borderRadius: BorderRadius.circular(10),
        onTap: () => context.go('/merge'),
        child: Padding(
          padding: const EdgeInsets.fromLTRB(14, 12, 14, 12),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Icon(Icons.merge_type, size: 15, color: accent),
                  const SizedBox(width: 6),
                  Text(
                    'Your PRs',
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: accent,
                      fontWeight: FontWeight.w600,
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 6),
              Text(
                summary.total == 1
                    ? '1 of your PRs needs you'
                    : '${summary.total} of your PRs need you',
                style: theme.textTheme.bodyMedium?.copyWith(
                  fontWeight: FontWeight.w500,
                ),
              ),
              const SizedBox(height: 2),
              Text(
                summary.describe(),
                style: theme.textTheme.bodySmall?.copyWith(color: muted),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

/// A PR on the board: what its review says, the PR, and the action that fits.
/// Clicking it opens the PR.
class _BoardCard extends ConsumerStatefulWidget {
  final InstanceGroup<PR> group;
  final bool selected;
  const _BoardCard({super.key, required this.group, required this.selected});

  @override
  ConsumerState<_BoardCard> createState() => _BoardCardState();
}

class _BoardCardState extends ConsumerState<_BoardCard>
    with _PrActions<_BoardCard> {
  @override
  InstanceGroup<PR> get group => widget.group;

  @override
  Widget build(BuildContext context) {
    final pr = _pr;
    final theme = Theme.of(context);
    final failure = currentFailure(pr);
    final isReviewing =
        (pr.reviewStatus?.active ?? false) ||
        ref.watch(reviewingPRsProvider).containsKey(_reviewKey);
    final status = prStatusOf(pr, reviewing: isReviewing);
    final statusColor = resolveAppColor(context, status.color);
    final accent = resolveAppColor(context, AppColors.accent);
    final muted = resolveAppColor(context, AppColors.textMuted);
    final danger = resolveAppColor(context, AppColors.danger);
    final small = theme.textTheme.bodySmall;
    final review = pr.latestReview;
    final open = pr.state == 'open';
    final abandoned =
        open && review?.publication == ReviewPublication.abandoned;
    final showFindings = review != null && !isReviewing && failure == null;

    Widget actionButton(String label, IconData icon, VoidCallback? onPressed) =>
        SizedBox(
          height: 30,
          child: OutlinedButton.icon(
            icon: Icon(icon, size: 15),
            label: Text(label),
            style: OutlinedButton.styleFrom(
              padding: const EdgeInsets.symmetric(horizontal: 10),
              textStyle: theme.textTheme.labelMedium,
            ),
            onPressed: onPressed,
          ),
        );
    final Widget? action = isReviewing
        ? actionButton(
            _cancelling ? 'Cancelling…' : 'Cancel',
            Icons.stop_circle_outlined,
            _cancelling ? null : _cancelReview,
          )
        : failure != null
        ? actionButton('Retry', Icons.refresh, _triggerReview)
        : review == null && open
        ? actionButton('Review now', Icons.play_arrow, _triggerReview)
        : null;

    return Material(
      color: resolveAppColor(context, AppColors.surfaceRaised),
      clipBehavior: Clip.antiAlias,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(10),
        side: BorderSide(
          color: widget.selected
              ? accent
              : resolveAppColor(context, AppColors.border),
          width: widget.selected ? 2 : 1,
        ),
      ),
      child: InkWell(
        onTap: () => context.push(prDetailRoute(pr.id, _instanceId)),
        child: Opacity(
          opacity: open ? 1 : 0.6,
          child: Padding(
            padding: const EdgeInsets.fromLTRB(14, 6, 4, 12),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    if (isReviewing)
                      SizedBox(
                        width: 13,
                        height: 13,
                        child: CircularProgressIndicator(
                          strokeWidth: 2,
                          color: statusColor,
                        ),
                      )
                    else
                      Icon(status.icon, size: 15, color: statusColor),
                    const SizedBox(width: 6),
                    Expanded(
                      child: Text(
                        status.label,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: small?.copyWith(
                          color: statusColor,
                          fontWeight: FontWeight.w600,
                        ),
                      ),
                    ),
                    Text(
                      relativeTime(pr.updatedAt, DateTime.now()),
                      style: small?.copyWith(color: muted),
                    ),
                    IconButton(
                      icon: const Icon(Icons.close, size: 15),
                      tooltip: 'Dismiss PR',
                      color: muted,
                      visualDensity: VisualDensity.compact,
                      onPressed: _dismiss,
                    ),
                  ],
                ),
                Padding(
                  padding: const EdgeInsets.only(right: 10),
                  child: Text(
                    pr.title,
                    maxLines: 2,
                    overflow: TextOverflow.ellipsis,
                    style: theme.textTheme.bodyMedium?.copyWith(
                      fontWeight: FontWeight.w500,
                      height: 1.35,
                    ),
                  ),
                ),
                const SizedBox(height: 4),
                Padding(
                  padding: const EdgeInsets.only(right: 10),
                  child: Text(
                    '${pr.repo} #${pr.number} · ${pr.author}'
                    '${open ? '' : ' · ${pr.state}'}',
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: small?.copyWith(color: muted),
                  ),
                ),
                if (group.members.isNotEmpty) ...[
                  const SizedBox(height: 6),
                  InstanceBadges(
                    instances: [
                      for (final m in group.orderedMembers)
                        (id: m.instanceId, name: m.instanceName),
                    ],
                    compact: true,
                  ),
                ],
                if (isReviewing) ...[
                  const SizedBox(height: 10),
                  Padding(
                    padding: const EdgeInsets.only(right: 10),
                    child: ClipRRect(
                      borderRadius: BorderRadius.circular(2),
                      child: LinearProgressIndicator(
                        minHeight: 3,
                        color: accent,
                        backgroundColor: accent.withValues(alpha: 0.16),
                      ),
                    ),
                  ),
                ] else if (failure != null) ...[
                  const SizedBox(height: 6),
                  Padding(
                    padding: const EdgeInsets.only(right: 10),
                    child: Tooltip(
                      message: failure.error,
                      child: Text(
                        reviewFailureSummary(failure),
                        maxLines: 3,
                        overflow: TextOverflow.ellipsis,
                        style: small?.copyWith(color: danger),
                      ),
                    ),
                  ),
                ] else if (abandoned) ...[
                  const SizedBox(height: 6),
                  Text(
                    'GitHub refused the review for good',
                    style: small?.copyWith(color: muted),
                  ),
                ],
                if (showFindings || action != null) ...[
                  const SizedBox(height: 10),
                  Padding(
                    padding: const EdgeInsets.only(right: 10),
                    child: Row(
                      children: [
                        if (showFindings)
                          Expanded(child: _FindingChips(review: review))
                        else
                          const Spacer(),
                        ?action,
                      ],
                    ),
                  ),
                ],
              ],
            ),
          ),
        ),
      ),
    );
  }
}

/// "2 high · 1 medium" as tinted chips; nothing when the review found nothing.
class _FindingChips extends StatelessWidget {
  final Review review;
  const _FindingChips({required this.review});

  @override
  Widget build(BuildContext context) {
    final counts = <String, int>{};
    for (final issue in review.issues) {
      final s = issue.severity.toLowerCase();
      counts[s] = (counts[s] ?? 0) + 1;
    }
    return Wrap(
      spacing: 4,
      runSpacing: 4,
      children: [
        for (final (severity, token) in const [
          ('high', AppColors.danger),
          ('medium', AppColors.warning),
          ('low', AppColors.success),
        ])
          if (counts[severity] != null)
            Builder(
              builder: (context) {
                final color = resolveAppColor(context, token);
                return AppBadge(
                  label: '${counts[severity]} $severity',
                  foreground: color,
                  background: color.withValues(alpha: 0.14),
                  padding: const EdgeInsets.symmetric(
                    horizontal: 7,
                    vertical: 2,
                  ),
                  radius: 5,
                  fontSize: 11,
                  letterSpacing: 0,
                );
              },
            ),
      ],
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
