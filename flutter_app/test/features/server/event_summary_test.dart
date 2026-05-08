import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/features/server/event_summary.dart';

void main() {
  group('summarize', () {
    test('review_started includes repo, number, agent', () {
      final s = summarize('review_started', {
        'pr_id': 1,
        'repo': 'acme/foo',
        'number': 42,
        'agent': 'claude',
      });
      expect(s, contains('acme/foo'));
      expect(s, contains('#42'));
      expect(s, contains('claude'));
    });

    test('review_completed includes duration when present', () {
      final s = summarize('review_completed', {
        'repo': 'acme/foo',
        'number': 42,
        'duration_ms': 4200,
      });
      expect(s, contains('acme/foo'));
      expect(s, contains('#42'));
      expect(s, contains('4.2s'));
    });

    test('issue_promoted includes stage transition when payload has it', () {
      final s = summarize('issue_promoted', {
        'repo': 'acme/foo',
        'number': 7,
        'from_stage': 'triage',
        'to_stage': 'refinement',
      });
      expect(s, contains('acme/foo'));
      expect(s, contains('#7'));
      expect(s, contains('triage'));
      expect(s, contains('refinement'));
    });

    test('polling_started includes kind and repo count', () {
      final s = summarize('polling_started', {
        'kind': 'prs',
        'repos': ['acme/foo', 'acme/bar'],
      });
      expect(s, contains('prs'));
      expect(s, contains('2'));
    });

    test('polling_completed includes kind, count, duration', () {
      final s = summarize('polling_completed', {
        'kind': 'issues',
        'count': 5,
        'duration_ms': 800,
      });
      expect(s, contains('issues'));
      expect(s, contains('5'));
      expect(s, contains('800'));
    });

    test('circuit_breaker_tripped includes reason', () {
      final s = summarize('circuit_breaker_tripped', {
        'reason': '5 failures/30s',
      });
      expect(s, contains('5 failures/30s'));
    });

    test('unknown event type falls back to type-only', () {
      final s = summarize('mystery_event', {'foo': 'bar'});
      expect(s, contains('mystery_event'));
    });
  });

  group('glyphFor', () {
    test('known types return non-empty icon and a color', () {
      for (final t in [
        'pr_detected',
        'review_started',
        'review_completed',
        'review_error',
        'issue_promoted',
        'polling_started',
        'polling_completed',
        'circuit_breaker_tripped',
      ]) {
        final g = glyphFor(t);
        expect(g.icon, isNotNull, reason: '$t has no icon');
      }
    });

    test('unknown types fall back to a default glyph', () {
      final g = glyphFor('mystery');
      expect(g.icon, isNotNull);
    });
  });
}
