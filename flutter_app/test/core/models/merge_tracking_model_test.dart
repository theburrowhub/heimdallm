import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/merge_tracking.dart';

/// A payload with every field populated, matching what the daemon sends.
Map<String, dynamic> fullEntryJson() => {
  'pr_id': 7,
  'repo': 'acme/widgets',
  'number': 42,
  'title': 'Add widget cache',
  'url': 'https://github.com/acme/widgets/pull/42',
  'author': 'octocat',
  'phase': 'blocked',
  'head_sha': 'abc123',
  'base_ref': 'main',
  'head_ref': 'feature',
  'block_reason': 'checks_failing',
  'block_detail': '1 required check is failing: build (GitHub Actions)',
  'is_author': true,
  'is_assignee': true,
  'excluded': false,
  'checks_required_failing': 1,
  'checks_required_pending': 2,
  'auto_merge_armed_at': '2026-08-28T11:00:00Z',
  'auto_merge_method': 'squash',
  'pre_rebase_sha': 'abc123def',
  'last_error': 'connection reset',
  'evaluated_at': '2026-08-28T12:00:00Z',
  'merged_at': '2026-08-28T13:00:00Z',
  'decision': {
    'ready': false,
    'blocks': [
      {'reason': 'checks_failing', 'detail': 'build is failing'},
    ],
    'checks': [
      {
        'name': 'build',
        'kind': 'check_run',
        'state': 'failure',
        'required': true,
        'description': 'the build',
        'app': 'GitHub Actions',
        'url': 'https://ci/build',
        'started_at': '2026-08-28T10:00:00Z',
        'completed_at': '2026-08-28T10:05:30Z',
      },
      {'name': 'coverage', 'state': 'failure', 'required': false},
    ],
    'checks_summary': {
      'total': 2,
      'required_total': 2,
      'required_success': 0,
      'required_pending': 1,
      'required_failing': 1,
      'optional_failing': 1,
      'missing_required': ['e2e'],
      'truncated': false,
    },
  },
};

void main() {
  test('MergeTrackingEntry round-trips every field', () {
    final entry = MergeTrackingEntry.fromJson(fullEntryJson());

    expect(entry.prId, 7);
    expect(entry.repo, 'acme/widgets');
    expect(entry.number, 42);
    expect(entry.title, 'Add widget cache');
    expect(entry.url, contains('/pull/42'));
    expect(entry.author, 'octocat');
    expect(entry.phase, 'blocked');
    expect(entry.headSha, 'abc123');
    expect(entry.baseRef, 'main');
    expect(entry.headRef, 'feature');
    expect(entry.blockReason, 'checks_failing');
    expect(entry.blockDetail, contains('build'));
    expect(entry.isAuthor, isTrue);
    expect(entry.isAssignee, isTrue);
    expect(entry.excluded, isFalse);
    expect(entry.checksRequiredFailing, 1);
    expect(entry.checksRequiredPending, 2);
    expect(entry.autoMergeArmedAt, isNotNull);
    expect(entry.autoMergeMethod, 'squash');
    expect(entry.preRebaseSha, 'abc123def');
    expect(entry.lastError, 'connection reset');
    expect(entry.evaluatedAt, isNotNull);
    expect(entry.mergedAt, isNotNull);
    expect(entry.decision, isNotNull);

    // toJson is what a client would send back; it must not lose fields.
    final round = MergeTrackingEntry.fromJson(entry.toJson());
    expect(round.prId, entry.prId);
    expect(round.blockDetail, entry.blockDetail);
    expect(round.checksRequiredFailing, entry.checksRequiredFailing);
    expect(round.preRebaseSha, entry.preRebaseSha);
    expect(round.decision?.checks.length, entry.decision?.checks.length);
  });

  test('MergeTrackingEntry defaults every optional field', () {
    final entry = MergeTrackingEntry.fromJson({
      'pr_id': 1,
      'repo': 'acme/widgets',
      'number': 1,
    });
    expect(entry.title, '');
    expect(entry.phase, 'idle');
    expect(entry.blockReason, '');
    expect(entry.checksRequiredFailing, 0);
    expect(entry.decision, isNull);
    expect(entry.autoMergeArmedAt, isNull);
    expect(entry.mergedAt, isNull);
  });

  test('MergeCheck round-trips and classifies its state', () {
    final json = fullEntryJson();
    final decision = MergeDecision.fromJson(
      json['decision'] as Map<String, dynamic>,
    );
    final build = decision.checks.first;

    expect(build.name, 'build');
    expect(build.kind, 'check_run');
    expect(build.app, 'GitHub Actions');
    expect(build.url, 'https://ci/build');
    expect(build.description, 'the build');
    expect(build.isFailure, isTrue);
    expect(build.isPending, isFalse);
    expect(build.isSuccess, isFalse);
    expect(build.duration, const Duration(minutes: 5, seconds: 30));

    final round = MergeCheck.fromJson(build.toJson());
    expect(round.name, build.name);
    expect(round.state, build.state);
    expect(round.duration, build.duration);
  });

  test('MergeCheck state predicates cover every value', () {
    MergeCheck of(String state) => MergeCheck(name: 'x', state: state);
    expect(of('success').isSuccess, isTrue);
    expect(of('neutral').isSuccess, isTrue);
    expect(of('pending').isPending, isTrue);
    expect(of('failure').isFailure, isTrue);
    expect(of('failure').isSuccess, isFalse);
  });

  test('MergeCheck duration is null unless both ends are known', () {
    final start = DateTime.utc(2026, 8, 28, 10);
    expect(MergeCheck(name: 'x', startedAt: start).duration, isNull);
    expect(MergeCheck(name: 'x', completedAt: start).duration, isNull);
    expect(const MergeCheck(name: 'x').duration, isNull);
    // Clock skew between GitHub's reporters is real; a negative duration is
    // nonsense rather than something to render.
    expect(
      MergeCheck(
        name: 'x',
        startedAt: start.add(const Duration(minutes: 1)),
        completedAt: start,
      ).duration,
      isNull,
    );
  });

  test('MergeChecksSummary round-trips and reports problems', () {
    final json = fullEntryJson();
    final decision = MergeDecision.fromJson(
      json['decision'] as Map<String, dynamic>,
    );
    final s = decision.checksSummary!;

    expect(s.total, 2);
    expect(s.requiredTotal, 2);
    expect(s.requiredFailing, 1);
    expect(s.requiredPending, 1);
    expect(s.optionalFailing, 1);
    expect(s.missingRequired, ['e2e']);
    expect(s.truncated, isFalse);
    expect(s.anyProblem, isTrue);

    final round = MergeChecksSummary.fromJson(s.toJson());
    expect(round.toJson(), s.toJson());
  });

  test('MergeChecksSummary.anyProblem ignores optional failures', () {
    // An optional red check is worth showing but does not hold up the merge,
    // so it must not raise the listing warning on its own.
    const optionalOnly = MergeChecksSummary(
      total: 2,
      requiredTotal: 1,
      requiredSuccess: 1,
      optionalFailing: 1,
    );
    expect(optionalOnly.anyProblem, isFalse);

    expect(const MergeChecksSummary(truncated: true).anyProblem, isTrue);
    expect(
      const MergeChecksSummary(missingRequired: ['e2e']).anyProblem,
      isTrue,
    );
    expect(const MergeChecksSummary().anyProblem, isFalse);
  });

  test('MergeBlock round-trips', () {
    final b = MergeBlock.fromJson({'reason': 'draft', 'detail': 'it is a draft'});
    expect(b.reason, 'draft');
    expect(b.detail, 'it is a draft');
    expect(MergeBlock.fromJson(b.toJson()).reason, 'draft');
    expect(MergeBlock.fromJson({'reason': 'draft'}).detail, '');
  });

  test('MergeDecision splits required from optional checks', () {
    final decision = MergeDecision.fromJson(
      fullEntryJson()['decision'] as Map<String, dynamic>,
    );
    expect(decision.ready, isFalse);
    expect(decision.blocks.single.reason, 'checks_failing');
    expect(decision.requiredChecks.map((c) => c.name), ['build']);
    expect(decision.optionalChecks.map((c) => c.name), ['coverage']);

    final round = MergeDecision.fromJson(decision.toJson());
    expect(round.checks.length, decision.checks.length);
    expect(round.blocks.length, decision.blocks.length);
  });

  test('MergeDecision defaults when the daemon omits the optional lists', () {
    final d = MergeDecision.fromJson({});
    expect(d.ready, isFalse);
    expect(d.blocks, isEmpty);
    expect(d.checks, isEmpty);
    expect(d.checksSummary, isNull);
    expect(d.requiredChecks, isEmpty);
    expect(d.optionalChecks, isEmpty);
  });

  // The listing warning turns on this getter, so its edges matter.
  test('blockedByChecks covers the reasons and the failing count', () {
    MergeTrackingEntry of({String reason = '', int failing = 0, String phase = 'blocked'}) =>
        MergeTrackingEntry(
          prId: 1,
          repo: 'r',
          number: 1,
          phase: phase,
          blockReason: reason,
          checksRequiredFailing: failing,
        );

    for (final reason in [
      'checks_failing',
      'checks_pending',
      'required_check_missing',
      'checks_unknown',
    ]) {
      expect(of(reason: reason).blockedByChecks, isTrue, reason: reason);
      expect(of(reason: reason).blockReasonIsChecks, isTrue, reason: reason);
    }

    // A failing check earns the warning even when something else is the
    // primary blocker.
    final behind = of(reason: 'behind_base', failing: 1);
    expect(behind.blockedByChecks, isTrue);
    expect(behind.blockReasonIsChecks, isFalse);

    expect(of(reason: 'behind_base').blockedByChecks, isFalse);
    // A merged PR keeps no warning, whatever its last counts were.
    expect(of(reason: 'checks_failing', failing: 3, phase: 'merged').blockedByChecks, isFalse);
  });

  test('entry state predicates', () {
    MergeTrackingEntry of(String phase) =>
        MergeTrackingEntry(prId: 1, repo: 'r', number: 1, phase: phase);

    expect(of('merged').isMerged, isTrue);
    expect(of('merged').isTerminal, isTrue);
    expect(of('abandoned').isTerminal, isTrue);
    expect(of('abandoned').isMerged, isFalse);
    expect(of('idle').isTerminal, isFalse);
    expect(of('auto_merge_armed').autoMergeArmed, isTrue);
    expect(of('idle').autoMergeArmed, isFalse);

    final counts = MergeTrackingEntry(
      prId: 1,
      repo: 'r',
      number: 1,
      checksRequiredFailing: 1,
      checksRequiredPending: 2,
    );
    expect(counts.hasFailingChecks, isTrue);
    expect(counts.hasPendingChecks, isTrue);
  });
}
