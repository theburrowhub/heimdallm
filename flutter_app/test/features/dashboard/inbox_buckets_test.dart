import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/issue.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/models/review.dart';
import 'package:heimdallm/core/models/review_status.dart';
import 'package:heimdallm/features/dashboard/inbox_buckets.dart';
import 'package:heimdallm/features/dashboard/inbox_sidebar.dart';
import 'package:heimdallm/shared/design_system/tokens.dart';

final _now = DateTime(2026, 11, 19, 23, 50);

Review _review({
  DateTime? at,
  int githubReviewId = 7,
  String state = 'APPROVED',
  String event = '',
  String severity = 'low',
  List<Issue> issues = const [],
}) => Review(
  id: 1,
  prId: 1,
  cliUsed: 'claude',
  summary: 'ok',
  issues: issues,
  severity: severity,
  createdAt: at ?? _now.subtract(const Duration(hours: 1)),
  githubReviewId: githubReviewId,
  githubReviewState: githubReviewId > 0 ? state : '',
  event: event,
);

ReviewExecutionStatus _status({String error = '', bool active = false}) =>
    ReviewExecutionStatus(
      headSha: 'abc',
      attempts: 1,
      failedAt: _now,
      retryAt: _now,
      error: error,
      active: active,
    );

PR _pr({
  String state = 'open',
  Review? review,
  ReviewExecutionStatus? status,
}) => PR(
  id: 1,
  githubId: 1,
  repo: 'acme/api',
  number: 1,
  title: 't',
  author: 'a',
  url: 'https://github.com/acme/api/pull/1',
  state: state,
  updatedAt: _now,
  latestReview: review,
  reviewStatus: status,
);

InboxBucket? _bucket(PR pr, {bool reviewing = false}) =>
    bucketOf(pr, reviewing: reviewing, now: _now);

void main() {
  group('bucketOf', () {
    test('an open PR whose review failed or was cancelled needs you', () {
      expect(
        _bucket(_pr(status: _status(error: 'boom'))),
        InboxBucket.needsYou,
      );
      expect(
        _bucket(
          _pr(
            review: _review(),
            status: _status(
              error: ReviewExecutionStatus.manualCancellationError,
            ),
          ),
        ),
        InboxBucket.needsYou,
      );
    });

    test('an open PR whose review GitHub rejected for good needs you', () {
      expect(
        _bucket(_pr(review: _review(githubReviewId: -1))),
        InboxBucket.needsYou,
      );
    });

    test('a failed PR that is being reviewed again is in progress', () {
      final failed = _pr(status: _status(error: 'boom'));
      expect(_bucket(failed, reviewing: true), InboxBucket.inProgress);
      expect(
        _bucket(_pr(status: _status(error: 'boom', active: true))),
        InboxBucket.inProgress,
      );
    });

    test('unreviewed and waiting-to-publish open PRs are in progress', () {
      expect(_bucket(_pr()), InboxBucket.inProgress);
      expect(
        _bucket(_pr(review: _review(githubReviewId: 0))),
        InboxBucket.inProgress,
      );
    });

    test(
      'a review posted today is done today, an older one is in no bucket',
      () {
        expect(_bucket(_pr(review: _review())), InboxBucket.doneToday);
        expect(
          _bucket(_pr(review: _review(at: DateTime(2026, 11, 19, 0, 5)))),
          InboxBucket.doneToday,
        );
        expect(
          _bucket(_pr(review: _review(at: DateTime(2026, 11, 18, 23, 59)))),
          isNull,
        );
      },
    );

    test('a failure older than the latest review is stale', () {
      // The daemon clears a failure only for the commit that later succeeded.
      final pr = _pr(
        review: _review(),
        status: ReviewExecutionStatus(
          headSha: 'old',
          attempts: 1,
          failedAt: _now.subtract(const Duration(hours: 3)),
          retryAt: _now,
          error: 'boom',
          active: false,
        ),
      );
      expect(currentFailure(pr), isNull);
      expect(_bucket(pr), InboxBucket.doneToday);
      expect(prStatusOf(pr, reviewing: false).label, 'Approved');
      // A run that failed after the review is current.
      final later = _pr(
        review: _review(at: _now.subtract(const Duration(hours: 5))),
        status: _status(error: 'boom'),
      );
      expect(currentFailure(later), isNotNull);
      expect(_bucket(later), InboxBucket.needsYou);
    });

    test('a closed PR never needs you nor is in progress', () {
      expect(
        _bucket(
          _pr(
            state: 'closed',
            status: _status(error: 'boom'),
          ),
        ),
        isNull,
      );
      expect(_bucket(_pr(state: 'closed')), isNull);
      expect(
        _bucket(_pr(state: 'closed', review: _review(githubReviewId: -1))),
        InboxBucket.doneToday,
      );
    });
  });

  group('prStatusOf', () {
    PrStatus status(PR pr, {bool reviewing = false}) =>
        prStatusOf(pr, reviewing: reviewing);

    test('a running review wins over everything else', () {
      final pr = _pr(
        status: _status(error: 'boom'),
        review: _review(),
      );
      expect(status(pr, reviewing: true).label, 'Reviewing…');
      expect(
        status(_pr(status: _status(active: true))).color,
        AppColors.accent,
      );
    });

    test('failures and cancellations', () {
      expect(
        status(_pr(status: _status(error: 'boom'))).label,
        'Review failed',
      );
      expect(
        status(
          _pr(
            status: _status(
              error: ReviewExecutionStatus.manualCancellationError,
            ),
          ),
        ).label,
        'Review cancelled',
      );
    });

    test('publication states', () {
      expect(status(_pr()).label, 'Not reviewed yet');
      expect(
        status(_pr(review: _review(githubReviewId: 0))).label,
        'Waiting to publish',
      );
      final rejected = status(_pr(review: _review(githubReviewId: -1)));
      expect(rejected.label, 'Not posted');
      expect(rejected.color, AppColors.danger);
      expect(
        status(_pr(state: 'closed', review: _review(githubReviewId: -1))).color,
        AppColors.textMuted,
      );
      expect(
        status(_pr(review: _review(githubReviewId: -2))).color,
        AppColors.textMuted,
      );
    });

    test('a posted review shows its verdict', () {
      String verdict(String state) =>
          status(_pr(review: _review(state: state))).label;
      expect(verdict('CHANGES_REQUESTED'), 'Changes requested');
      expect(verdict('APPROVED'), 'Approved');
      expect(verdict('COMMENTED'), 'Commented');
      expect(verdict('DISMISSED'), 'Review dismissed');
      // No state recorded: high severity implies changes were requested.
      expect(
        status(
          _pr(
            review: _review(state: '', severity: 'high'),
          ),
        ).label,
        'Changes requested',
      );
    });
  });

  test('findingCounts lists severities from high to low', () {
    Issue issue(String severity) =>
        Issue(file: 'a.go', line: 1, description: 'x', severity: severity);
    expect(findingCounts(_review()), '');
    expect(
      findingCounts(
        _review(issues: [issue('low'), issue('HIGH'), issue('high')]),
      ),
      '2 high · 1 low',
    );
  });

  test('weekReviews sums the daily counts of every instance', () {
    expect(weekReviews(const []), 0);
    expect(
      weekReviews([
        {
          'reviews_last_7_days': [
            {'day': '2026-11-18', 'count': 3},
            {'day': '2026-11-19', 'count': 1},
          ],
        },
        {
          'reviews_last_7_days': [
            {'day': '2026-11-19', 'count': 2},
          ],
        },
        // An older daemon without the field counts as none.
        <String, dynamic>{},
      ]),
      6,
    );
  });
}
