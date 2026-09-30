import 'merge_tracking.dart';

/// The sections of the My PRs tab, in display order.
enum MyPrsSection {
  /// Something to fix: CI, a review, conflicts, an out-of-date branch.
  action,

  /// Every requirement met and nobody is going to merge it: one click left.
  ready,

  /// Reviewers, CI, GitHub or Heimdallm's own automation have the ball.
  waiting,

  /// Merged or closed in the last day; collapsed by default.
  recent,
}

/// Which section [entry] belongs in. Stale is a flag on top of the section,
/// not a section of its own: a stale PR still waits on someone.
MyPrsSection sectionFor(MergeTrackingEntry entry) {
  if (entry.isTerminal) return MyPrsSection.recent;
  if (entry.excluded) return MyPrsSection.waiting;
  return switch (entry.attention) {
    'action' => MyPrsSection.action,
    'ready' => MyPrsSection.ready,
    _ => MyPrsSection.waiting,
  };
}

/// The My PRs listing split into its sections, each keeping the daemon's order
/// (CI problems first) except that stale PRs lead within their section.
class MyPrsGroups {
  final List<MergeTrackingEntry> action;
  final List<MergeTrackingEntry> ready;
  final List<MergeTrackingEntry> waiting;
  final List<MergeTrackingEntry> recent;

  const MyPrsGroups({
    this.action = const [],
    this.ready = const [],
    this.waiting = const [],
    this.recent = const [],
  });

  List<MergeTrackingEntry> of(MyPrsSection section) => switch (section) {
    MyPrsSection.action => action,
    MyPrsSection.ready => ready,
    MyPrsSection.waiting => waiting,
    MyPrsSection.recent => recent,
  };

  bool get isEmpty =>
      action.isEmpty && ready.isEmpty && waiting.isEmpty && recent.isEmpty;
}

MyPrsGroups groupMyPrs(Iterable<MergeTrackingEntry> entries) {
  final buckets = {
    for (final s in MyPrsSection.values) s: <MergeTrackingEntry>[],
  };
  for (final e in entries) {
    buckets[sectionFor(e)]!.add(e);
  }
  List<MergeTrackingEntry> staleFirst(List<MergeTrackingEntry> list) {
    // A stable partition, so the daemon's own ordering survives otherwise.
    return [...list.where((e) => e.stale), ...list.where((e) => !e.stale)];
  }

  final recent = buckets[MyPrsSection.recent]!
    ..sort((a, b) {
      final at = a.terminalAt ?? a.mergedAt;
      final bt = b.terminalAt ?? b.mergedAt;
      if (at == null && bt == null) return 0;
      if (at == null) return 1;
      if (bt == null) return -1;
      return bt.compareTo(at); // newest first
    });
  return MyPrsGroups(
    action: staleFirst(buckets[MyPrsSection.action]!),
    ready: staleFirst(buckets[MyPrsSection.ready]!),
    waiting: staleFirst(buckets[MyPrsSection.waiting]!),
    recent: recent,
  );
}

/// Counts of the PRs that ask for the operator, across every instance.
class MyPrsSummary {
  final int needAction;
  final int ready;
  final int stale;

  /// Distinct PRs behind the counts: a PR that needs action AND is stale is
  /// one PR, not two.
  final int total;

  const MyPrsSummary({
    this.needAction = 0,
    this.ready = 0,
    this.stale = 0,
    this.total = 0,
  });

  bool get isEmpty => total == 0;

  /// One line: "2 need your action · 1 ready to merge · 3 stale".
  String describe() {
    final parts = <String>[
      if (needAction > 0)
        '$needAction need${needAction == 1 ? 's' : ''} your action',
      if (ready > 0) '$ready ready to merge',
      if (stale > 0) '$stale stale',
    ];
    return parts.join(' · ');
  }

  @override
  bool operator ==(Object other) =>
      other is MyPrsSummary &&
      other.needAction == needAction &&
      other.ready == ready &&
      other.stale == stale &&
      other.total == total;

  @override
  int get hashCode => Object.hash(needAction, ready, stale, total);
}

MyPrsSummary summarizeMyPrs(Iterable<MergeTrackingEntry> entries) {
  var action = 0, ready = 0, stale = 0, total = 0;
  for (final e in entries) {
    if (!e.needsOperator) continue;
    total++;
    if (e.needsAction) action++;
    if (e.isReadyToMerge) ready++;
    if (e.stale) stale++;
  }
  return MyPrsSummary(
    needAction: action,
    ready: ready,
    stale: stale,
    total: total,
  );
}

/// The PRs worth surfacing outside the tab (tray, digest), most urgent first:
/// action, then ready, then stale-but-waiting.
List<MergeTrackingEntry> myPrsNeedingOperator(
  Iterable<MergeTrackingEntry> entries,
) {
  final list = entries.where((e) => e.needsOperator).toList();
  int rank(MergeTrackingEntry e) => e.needsAction
      ? 0
      : e.isReadyToMerge
      ? 1
      : 2;
  list.sort((a, b) => rank(a).compareTo(rank(b)));
  return list;
}

/// A compact idle duration: "45m", "5h", "4d".
String formatIdle(Duration d) {
  if (d.inDays >= 1) return '${d.inDays}d';
  if (d.inHours >= 1) return '${d.inHours}h';
  final m = d.inMinutes < 1 ? 1 : d.inMinutes;
  return '${m}m';
}

/// How long [entry] has gone without activity, measured against [now].
Duration? idleFor(MergeTrackingEntry entry, DateTime now) {
  final last = entry.lastActivityAt;
  if (last == null) return null;
  final d = now.difference(last);
  return d.isNegative ? Duration.zero : d;
}

/// A few words on why [entry] needs the operator, for the tray and
/// notifications where a full sentence does not fit.
String shortAttentionLabel(MergeTrackingEntry entry, {DateTime? now}) {
  if (entry.isReadyToMerge) return 'ready to merge';
  if (entry.needsAction) {
    return switch (entry.blockReason) {
      'changes_requested' => 'changes requested',
      'checks_failing' => 'CI failing',
      'required_check_missing' => 'required check missing',
      'conflicts' => 'conflicts',
      'behind_base' => 'behind base',
      'unresolved_threads' => 'unresolved threads',
      'draft' => 'still a draft',
      'attempt_cap_reached' => 'automation gave up',
      _ => 'needs you',
    };
  }
  if (entry.stale) {
    final idle = idleFor(entry, now ?? DateTime.now());
    return idle == null ? 'stale' : 'stale · ${formatIdle(idle)}';
  }
  return '';
}
