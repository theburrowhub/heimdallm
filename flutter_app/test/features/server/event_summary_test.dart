import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/features/server/event_summary.dart';

void main() {
  group('format', () {
    test('review_started returns human label, target, and agent detail', () {
      final ev = format('review_started', {
        'pr_id': 1,
        'repo': 'acme/foo',
        'number': 42,
        'agent': 'claude',
      });
      expect(ev.label, 'Review started');
      expect(ev.target, 'acme/foo#42');
      expect(ev.details, contains('claude'));
      expect(ev.status, EventStatus.started);
    });

    test('review_completed includes duration when present', () {
      final ev = format('review_completed', {
        'repo': 'acme/foo',
        'number': 42,
        'duration_ms': 4200,
      });
      expect(ev.label, 'Review completed');
      expect(ev.target, 'acme/foo#42');
      expect(ev.details, contains('4.2s'));
      expect(ev.status, EventStatus.succeeded);
    });

    test('review_completed omits duration chip when payload lacks it', () {
      final ev = format('review_completed', {
        'repo': 'acme/foo',
        'number': 42,
      });
      expect(ev.details, isEmpty);
    });

    test('review_error surfaces the error reason in details', () {
      final ev = format('review_error', {
        'repo': 'acme/foo',
        'number': 42,
        'error': 'connection refused',
      });
      expect(ev.label, 'Review failed');
      expect(ev.status, EventStatus.failed);
      expect(ev.details, contains('connection refused'));
    });

    test('review_error truncates very long error messages', () {
      final ev = format('review_error', {
        'repo': 'acme/foo',
        'error': 'x' * 200,
      });
      expect(ev.details.length, 1);
      expect(ev.details.first.length, lessThanOrEqualTo(80));
      expect(ev.details.first, endsWith('…'));
    });

    test('issue_promoted spells out the stage transition', () {
      final ev = format('issue_promoted', {
        'repo': 'acme/foo',
        'number': 7,
        'from_stage': 'triage',
        'to_stage': 'refinement',
        'trigger': 'auto-promote',
      });
      expect(ev.label, 'Stage promoted');
      expect(ev.target, 'acme/foo#7');
      expect(ev.details, contains('triage → refinement'));
      expect(ev.details, contains('auto-promote'));
      expect(ev.status, EventStatus.warning);
    });

    test('polling_started includes kind and repo count', () {
      final ev = format('polling_started', {
        'kind': 'prs',
        'repos': ['acme/foo', 'acme/bar'],
      });
      expect(ev.label, 'Polling started');
      expect(ev.target, isEmpty);
      expect(ev.details, containsAllInOrder(<String>['prs', '2 repos']));
      expect(ev.status, EventStatus.started);
    });

    test('polling_completed includes kind, count, and duration', () {
      final ev = format('polling_completed', {
        'kind': 'issues',
        'count': 5,
        'duration_ms': 800,
      });
      expect(ev.label, 'Polling completed');
      expect(ev.details, contains('issues'));
      expect(ev.details, contains('5 items'));
      expect(ev.details, contains('800ms'));
      expect(ev.status, EventStatus.succeeded);
    });

    test('circuit_breaker_tripped surfaces reason and uses warning status', () {
      final ev = format('circuit_breaker_tripped', {
        'reason': '5 failures/30s',
      });
      expect(ev.label, 'Circuit breaker tripped');
      expect(ev.details, contains('5 failures/30s'));
      expect(ev.status, EventStatus.warning);
    });

    test('repo_discovered uses repo as target without number', () {
      final ev = format('repo_discovered', {
        'repo': 'acme/foo',
      });
      expect(ev.label, 'Repo discovered');
      expect(ev.target, 'acme/foo');
      expect(ev.details, isEmpty);
    });

    test('pr_state_changed shows the transition chip', () {
      final ev = format('pr_state_changed', {
        'repo': 'acme/foo',
        'number': 42,
        'old_state': 'open',
        'new_state': 'closed',
      });
      expect(ev.label, 'PR state changed');
      expect(ev.details, contains('open → closed'));
    });

    test('issue_review_started maps to Triage started label', () {
      // The fetcher publishes review_only as "Triage" in the staged
      // pipeline language — keep the label aligned with how operators
      // refer to the stage in the docs and the issue tracking UI.
      final ev = format('issue_review_started', {
        'repo': 'acme/foo',
        'number': 7,
      });
      expect(ev.label, 'Triage started');
    });

    test('unknown event type degrades gracefully to its raw type', () {
      // The renderer must still draw the row — losing visibility on a
      // brand-new event type is worse than showing its raw identifier.
      final ev = format('mystery_event', {'foo': 'bar', 'repo': 'acme/foo'});
      expect(ev.label, 'mystery_event');
      expect(ev.target, 'acme/foo');
      expect(ev.status, EventStatus.info);
    });
  });
}
