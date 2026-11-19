import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/review.dart';

Map<String, dynamic> _json([Map<String, dynamic> extra = const {}]) => {
  'id': 1,
  'pr_id': 2,
  'cli_used': 'claude',
  'summary': 'ok',
  'issues': <dynamic>[],
  'severity': 'low',
  'created_at': '2026-11-19T10:00:00Z',
  ...extra,
};

Review _review({int id = 0, String state = '', String event = ''}) => Review(
  id: 1,
  prId: 2,
  cliUsed: 'claude',
  summary: 'ok',
  issues: const [],
  severity: 'low',
  createdAt: DateTime.utc(2026, 11, 19),
  githubReviewId: id,
  githubReviewState: state,
  event: event,
);

void main() {
  test('parses the publication fields the daemon sends', () {
    final r = Review.fromJson(
      _json({
        'github_review_id': 4242,
        'github_review_state': 'CHANGES_REQUESTED',
        'event': 'REQUEST_CHANGES',
        'head_sha': '0123456789abcdef',
        // A Go zero time: publication must not be read from this.
        'published_at': '0001-01-01T00:00:00Z',
      }),
    );
    expect(r.githubReviewId, 4242);
    expect(r.githubReviewState, 'CHANGES_REQUESTED');
    expect(r.event, 'REQUEST_CHANGES');
    expect(r.headSha, '0123456789abcdef');
    expect(r.shortSha, '0123456');
    expect(r.publication, ReviewPublication.posted);
    expect(r.verdict, ReviewVerdict.changesRequested);
  });

  test('a payload without the fields still parses, as not posted yet', () {
    final r = Review.fromJson(_json());
    expect(r.githubReviewId, 0);
    expect(r.githubReviewState, '');
    expect(r.event, '');
    expect(r.headSha, '');
    expect(r.shortSha, '');
    expect(r.publication, ReviewPublication.pending);
    expect(r.verdict, isNull);
  });

  test('publication follows the daemon review id sentinels', () {
    expect(_review(id: 7).publication, ReviewPublication.posted);
    expect(_review(id: 0).publication, ReviewPublication.pending);
    expect(_review(id: -1).publication, ReviewPublication.abandoned);
    expect(_review(id: -2).publication, ReviewPublication.superseded);
    // An unknown negative sentinel is terminal too: never "pending".
    expect(_review(id: -9).publication, ReviewPublication.abandoned);
  });

  test('verdict prefers GitHub state and falls back to the decided event', () {
    expect(_review(state: 'APPROVED').verdict, ReviewVerdict.approved);
    expect(_review(state: 'commented').verdict, ReviewVerdict.commented);
    expect(_review(state: 'DISMISSED').verdict, ReviewVerdict.dismissed);
    // GitHub's state wins over the decided event once posted.
    expect(
      _review(state: 'DISMISSED', event: 'APPROVE').verdict,
      ReviewVerdict.dismissed,
    );
    expect(_review(event: 'APPROVE').verdict, ReviewVerdict.approved);
    expect(
      _review(event: 'REQUEST_CHANGES').verdict,
      ReviewVerdict.changesRequested,
    );
    expect(_review(event: 'COMMENT').verdict, ReviewVerdict.commented);
    expect(_review(state: 'PENDING').verdict, isNull);
  });

  test('a short head SHA is kept whole', () {
    expect(Review.fromJson(_json({'head_sha': 'abc'})).shortSha, 'abc');
  });

  test('peer rows are recognised', () {
    expect(Review.fromJson(_json({'cli_used': 'peer'})).isPeer, isTrue);
    expect(Review.fromJson(_json()).isPeer, isFalse);
  });
}
