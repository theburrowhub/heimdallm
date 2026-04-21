import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/activity.dart';

void main() {
  group('ActivityEntry.fromJson', () {
    test('parses a full entry', () {
      final json = {
        'id': 1,
        'ts': '2026-04-20T09:34:12+02:00',
        'org': 'acme',
        'repo': 'acme/api',
        'item_type': 'pr',
        'item_number': 42,
        'item_title': 'Fix bug',
        'action': 'review',
        'outcome': 'major',
        'details': {'cli_used': 'claude'},
      };
      final e = ActivityEntry.fromJson(json);
      expect(e.id, 1);
      expect(e.repo, 'acme/api');
      expect(e.itemNumber, 42);
      expect(e.action, ActivityAction.review);
      expect(e.outcome, 'major');
      expect(e.details['cli_used'], 'claude');
    });

    test('unknown action falls back', () {
      final e = ActivityEntry.fromJson({
        'id': 1,
        'ts': '2026-04-20T09:34:12+02:00',
        'org': 'a',
        'repo': 'a/b',
        'item_type': 'pr',
        'item_number': 1,
        'item_title': 't',
        'action': 'frobnicate',
        'outcome': '',
        'details': {},
      });
      expect(e.action, ActivityAction.unknown);
    });

    test('missing outcome defaults to empty string', () {
      final e = ActivityEntry.fromJson({
        'id': 1,
        'ts': '2026-04-20T09:34:12+02:00',
        'org': 'a',
        'repo': 'a/b',
        'item_type': 'pr',
        'item_number': 1,
        'item_title': 't',
        'action': 'review',
        'details': {},
      });
      expect(e.outcome, '');
    });
  });

  group('ActivityPage.fromJson', () {
    test('parses empty page', () {
      final p = ActivityPage.fromJson({
        'entries': [],
        'count': 0,
        'truncated': false,
      });
      expect(p.entries, isEmpty);
      expect(p.count, 0);
      expect(p.truncated, isFalse);
    });

    test('parses populated page with truncation', () {
      final p = ActivityPage.fromJson({
        'entries': [
          {
            'id': 1,
            'ts': '2026-04-20T09:34:12+02:00',
            'org': 'a',
            'repo': 'a/b',
            'item_type': 'pr',
            'item_number': 1,
            'item_title': 't',
            'action': 'review',
            'outcome': 'minor',
            'details': {},
          }
        ],
        'count': 500,
        'truncated': true,
      });
      expect(p.entries.length, 1);
      expect(p.truncated, isTrue);
      expect(p.count, 500);
    });
  });

  group('ActivityQuery.toQueryParameters', () {
    test('emits date when set', () {
      final q = ActivityQuery(date: DateTime(2026, 4, 20));
      final p = q.toQueryParameters();
      expect(p['date'], ['2026-04-20']);
      expect(p.containsKey('from'), isFalse);
      expect(p.containsKey('to'), isFalse);
    });

    test('emits from/to when range set', () {
      final q = ActivityQuery(
        from: DateTime(2026, 4, 18),
        to: DateTime(2026, 4, 20),
      );
      final p = q.toQueryParameters();
      expect(p['from'], ['2026-04-18']);
      expect(p['to'], ['2026-04-20']);
      expect(p.containsKey('date'), isFalse);
    });

    test('multi-value filters', () {
      const q = ActivityQuery(
        orgs: {'a', 'b'},
        repos: {'a/x'},
        actions: {ActivityAction.review, ActivityAction.triage},
      );
      final p = q.toQueryParameters();
      expect(p['org']!.toSet(), {'a', 'b'});
      expect(p['repo'], ['a/x']);
      expect(p['action']!.toSet(), {'review', 'triage'});
    });

    test('always includes limit', () {
      expect(
        const ActivityQuery().toQueryParameters()['limit'],
        ['500'],
      );
      expect(
        const ActivityQuery(limit: 250).toQueryParameters()['limit'],
        ['250'],
      );
    });
  });

  group('ActivityQuery constructor', () {
    test('asserts on partial range (from only)', () {
      expect(
        () => ActivityQuery(from: DateTime(2026, 4, 18)),
        throwsA(isA<AssertionError>()),
      );
    });

    test('asserts on partial range (to only)', () {
      expect(
        () => ActivityQuery(to: DateTime(2026, 4, 18)),
        throwsA(isA<AssertionError>()),
      );
    });

    test('both null is fine', () {
      expect(() => const ActivityQuery(), returnsNormally);
    });
  });

  group('ActivityQuery.copyWith', () {
    test('keeps unspecified date/from/to', () {
      final q = ActivityQuery(date: DateTime(2026, 4, 20));
      final c = q.copyWith(orgs: {'a'});
      expect(c.date, DateTime(2026, 4, 20));
      expect(c.orgs, {'a'});
    });

    test('explicit null clears date', () {
      final q = ActivityQuery(date: DateTime(2026, 4, 20));
      final c = q.copyWith(date: null);
      expect(c.date, isNull);
    });

    test('explicit from/to both null clears range', () {
      final q = ActivityQuery(
        from: DateTime(2026, 4, 18),
        to: DateTime(2026, 4, 20),
      );
      final c = q.copyWith(from: null, to: null);
      expect(c.from, isNull);
      expect(c.to, isNull);
    });
  });
}
