import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/instances/aggregation.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:mocktail/mocktail.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../../core/platform/fake_platform_services.dart';

class _MockApiClient extends Mock implements ApiClient {}

/// Waits for a FutureProvider's first settled value.
///
/// Not `await container.read(p.future)`: these providers watch meProvider,
/// which settles a microtask later and triggers a rebuild. In the app that is
/// harmless — every consumer re-watches and picks up the new future — but a
/// one-shot read holds onto the discarded first future forever.
Future<T> settled<T>(
  ProviderContainer container,
  FutureProvider<T> provider,
) async {
  final completer = Completer<T>();
  final sub = container.listen<AsyncValue<T>>(
    provider,
    (previous, next) {
      next.whenOrNull(
        data: (value) {
          if (!completer.isCompleted) completer.complete(value);
        },
        error: (error, _) {
          if (!completer.isCompleted) completer.completeError(error);
        },
      );
    },
    fireImmediately: true,
  );
  try {
    return await completer.future.timeout(const Duration(seconds: 10));
  } finally {
    sub.close();
  }
}

PR _pr(int id, String repo, int number) => PR(
  id: id,
  githubId: 100 + id,
  repo: repo,
  number: number,
  title: 'PR $number',
  author: 'alice',
  url: 'https://github.com',
  state: 'open',
  updatedAt: DateTime(2026, 9, 1),
);

void main() {
  setUp(() {
    SharedPreferences.setMockInitialValues({});
    registerFallbackValue(<String>[]);
  });

  group('reviewKeyFor', () {
    test('is unqualified on a single-daemon install', () {
      // Preserving the old format exactly is what keeps the existing
      // single-daemon behaviour, and its tests, unchanged.
      expect(reviewKeyFor('', 'acme/tools', 42), 'acme/tools:42');
    });

    test('is instance-qualified once clustering is on', () {
      // Two instances can both hold the same repo and PR number; an
      // unqualified key would let one machine's spinner clear the other's.
      expect(reviewKeyFor('srv-a', 'acme/tools', 42), 'srv-a|acme/tools:42');
      expect(
        reviewKeyFor('srv-a', 'acme/tools', 42),
        isNot(reviewKeyFor('srv-b', 'acme/tools', 42)),
      );
    });
  });

  group('mergeStatsMaps', () {
    test('a single instance is passed through untouched', () {
      final one = {'reviews': 3, 'label': 'x'};
      expect(mergeStatsMaps([one]), same(one));
    });

    test('no instances yields an empty map', () {
      expect(mergeStatsMaps(const []), isEmpty);
    });

    test('sums numbers across the fleet', () {
      // A per-instance count is not the fleet's count.
      final merged = mergeStatsMaps([
        {'reviews': 3, 'issues': 1.5},
        {'reviews': 4, 'issues': 0.5},
      ]);
      expect(merged['reviews'], 7);
      expect(merged['issues'], 2.0);
    });

    test('takes the first non-numeric value rather than inventing one', () {
      // Averaging or concatenating a label would produce a value nobody
      // measured.
      final merged = mergeStatsMaps([
        {'mode': 'single', 'breakdown': {'a': 1}},
        {'mode': 'multi', 'breakdown': {'b': 2}},
      ]);
      expect(merged['mode'], 'single');
      expect(merged['breakdown'], {'a': 1});
    });

    test('keys present on only one instance survive', () {
      final merged = mergeStatsMaps([
        {'a': 1},
        {'b': 2},
      ]);
      expect(merged, {'a': 1, 'b': 2});
    });
  });

  group('clientForInstance', () {
    test('the empty id resolves through the overridable seam', () async {
      // Bypassing apiClientProvider here would make an injected client
      // silently ignored by every fan-out.
      final injected = _MockApiClient();
      final container = ProviderContainer(
        overrides: [
          platformServicesProvider.overrideWithValue(FakePlatformServices()),
          apiClientProvider.overrideWithValue(injected),
        ],
      );
      addTearDown(container.dispose);

      final resolved = container.read(apiClientProvider);
      expect(identical(resolved, injected), isTrue);
    });
  });

  group('aggregating providers', () {
    ProviderContainer containerWith(ApiClient api, {ClusterRegistry? registry}) {
      final container = ProviderContainer(
        overrides: [
          platformServicesProvider.overrideWithValue(FakePlatformServices()),
          apiClientProvider.overrideWithValue(api),
          daemonInstancesProvider.overrideWith(
            (ref) async => registry ?? ClusterRegistry.empty,
          ),
          meProvider.overrideWith((ref) async => 'alice'),
        ],
      );
      addTearDown(container.dispose);
      return container;
    }

    test('a single-daemon install fetches once and tags nothing', () async {
      final api = _MockApiClient();
      when(() => api.fetchPRs(states: any(named: 'states'))).thenAnswer(
        (_) async => [_pr(1, 'acme/tools', 7)],
      );

      final container = containerWith(api);
      final result = await settled(container, prsByInstanceProvider);

      expect(result.values, hasLength(1));
      expect(result.items.single.instanceId, isEmpty);
      expect(result.hasFailures, isFalse);
      // The flat list derives from it, so every existing consumer is unchanged.
      expect(await settled(container, prsProvider), hasLength(1));
    });

    test('one instance failing still yields the others', () async {
      final api = _MockApiClient();
      when(() => api.fetchPRs(states: any(named: 'states')))
          .thenThrow(ApiException('offline'));

      final container = containerWith(api);
      final result = await settled(container, prsByInstanceProvider);

      expect(result.values, isEmpty);
      expect(result.hasFailures, isTrue);
      // The failure is surfaced for the banner rather than swallowed.
      expect(container.read(instanceReadFailuresProvider), hasLength(1));
    });

    test('stats are summed across the fleet', () async {
      final api = _MockApiClient();
      when(
        () => api.fetchStats(
          repos: any(named: 'repos'),
          orgs: any(named: 'orgs'),
        ),
      ).thenAnswer((_) async => {'reviews': 5});

      final container = containerWith(api);
      expect(await settled(container, statsProvider), {'reviews': 5});
    });

    test('cachedMergedPRs reads the merged list without awaiting', () async {
      final api = _MockApiClient();
      when(() => api.fetchPRs(states: any(named: 'states'))).thenAnswer(
        (_) async => [_pr(1, 'acme/tools', 7), _pr(2, 'acme/tools', 8)],
      );

      final container = containerWith(api);
      await settled(container, prsByInstanceProvider);

      // The reconciliation and tray paths run synchronously and must see every
      // instance's PRs, not just the selected one.
      final prs = container.read(prsByInstanceProvider).value!.values;
      expect(prs, hasLength(2));
    });
  });

  test('InstanceFailure carries a renderable label', () {
    const failure = InstanceFailure(
      instanceId: 'srv-a',
      instanceName: '',
      error: 'boom',
    );
    expect(failure.label, 'srv-a');
  });
}
