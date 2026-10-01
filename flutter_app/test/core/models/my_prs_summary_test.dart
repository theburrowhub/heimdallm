import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/merge_tracking.dart';
import 'package:heimdallm/core/models/my_prs_summary.dart';

MergeTrackingEntry _pr(
  int n, {
  String attention = 'none',
  bool stale = false,
  bool excluded = false,
  String phase = 'blocked',
  String blockReason = '',
  DateTime? terminalAt,
  DateTime? lastActivityAt,
}) => MergeTrackingEntry(
  prId: n,
  repo: 'acme/widgets',
  number: n,
  phase: phase,
  attention: attention,
  stale: stale,
  excluded: excluded,
  blockReason: blockReason,
  terminalAt: terminalAt,
  lastActivityAt: lastActivityAt,
);

void main() {
  test('entries land in the section of who they wait on', () {
    expect(sectionFor(_pr(1, attention: 'action')), MyPrsSection.action);
    expect(sectionFor(_pr(1, attention: 'ready')), MyPrsSection.ready);
    expect(sectionFor(_pr(1, attention: 'waiting')), MyPrsSection.waiting);
    expect(sectionFor(_pr(1)), MyPrsSection.waiting);
    expect(sectionFor(_pr(1, attention: 'action', excluded: true)), MyPrsSection.waiting);
    expect(sectionFor(_pr(1, phase: 'merged', attention: 'action')), MyPrsSection.recent);
    expect(sectionFor(_pr(1, phase: 'abandoned')), MyPrsSection.recent);
  });

  test('stale PRs lead their section; recent is newest first', () {
    final g = groupMyPrs([
      _pr(1, attention: 'waiting'),
      _pr(2, attention: 'waiting', stale: true),
      _pr(3, phase: 'merged', terminalAt: DateTime(2026, 9, 30, 8)),
      _pr(4, phase: 'merged', terminalAt: DateTime(2026, 9, 30, 12)),
      _pr(5, phase: 'abandoned'),
    ]);
    expect(g.waiting.map((e) => e.number), [2, 1]);
    expect(g.recent.map((e) => e.number), [4, 3, 5]);
    expect(g.action, isEmpty);
    expect(g.isEmpty, isFalse);
    expect(groupMyPrs(const []).isEmpty, isTrue);
  });

  test('the summary counts each PR once and skips excluded or finished ones', () {
    final s = summarizeMyPrs([
      _pr(1, attention: 'action', stale: true),
      _pr(2, attention: 'ready'),
      _pr(3, attention: 'waiting', stale: true),
      _pr(4, attention: 'waiting'),
      _pr(5, attention: 'action', excluded: true),
      _pr(6, phase: 'merged', attention: 'ready'),
    ]);
    expect(s, const MyPrsSummary(needAction: 1, ready: 1, stale: 2, total: 3));
    expect(s.describe(), '1 needs your action · 1 ready to merge · 2 stale');
    expect(const MyPrsSummary(needAction: 2, total: 2).describe(), '2 need your action');
    expect(const MyPrsSummary().isEmpty, isTrue);
  });

  test('urgent PRs are ordered action, ready, then stale', () {
    final list = myPrsNeedingOperator([
      _pr(1, attention: 'waiting', stale: true),
      _pr(2, attention: 'ready'),
      _pr(3, attention: 'waiting'),
      _pr(4, attention: 'action'),
    ]);
    expect(list.map((e) => e.number), [4, 2, 1]);
  });

  test('formatIdle picks the largest whole unit', () {
    expect(formatIdle(const Duration(days: 4, hours: 5)), '4d');
    expect(formatIdle(const Duration(hours: 5, minutes: 50)), '5h');
    expect(formatIdle(const Duration(minutes: 45)), '45m');
    expect(formatIdle(const Duration(seconds: 10)), '1m');
  });

  test('short labels say what the PR needs', () {
    final now = DateTime(2026, 9, 30, 12);
    expect(shortAttentionLabel(_pr(1, attention: 'ready')), 'ready to merge');
    expect(
      shortAttentionLabel(_pr(1, attention: 'action', blockReason: 'changes_requested')),
      'changes requested',
    );
    expect(
      shortAttentionLabel(_pr(1, attention: 'action', blockReason: 'something_new')),
      'needs you',
    );
    expect(
      shortAttentionLabel(
        _pr(1, attention: 'waiting', stale: true, lastActivityAt: DateTime(2026, 9, 28, 12)),
        now: now,
      ),
      'stale · 2d',
    );
    expect(shortAttentionLabel(_pr(1, attention: 'waiting')), '');
    expect(idleFor(_pr(1, lastActivityAt: DateTime(2026, 10, 1)), now), Duration.zero);
    expect(idleFor(_pr(1), now), isNull);
  });
}
