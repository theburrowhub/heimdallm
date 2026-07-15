import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/api/sse_client.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:mocktail/mocktail.dart';

class MockApiClient extends Mock implements ApiClient {}

void main() {
  for (final eventType in ['repo_discovered', 'repo_renamed']) {
    test('$eventType refreshes config from the daemon', () async {
      final api = MockApiClient();
      var fetches = 0;
      when(() => api.fetchConfig()).thenAnswer((_) async {
        fetches++;
        return {
          'repositories': fetches == 1
              ? <String>['acme/old']
              : <String>['acme/old', 'acme/new'],
        };
      });

      final events = StreamController<SseEvent>.broadcast();
      final container = ProviderContainer(
        overrides: [
          apiClientProvider.overrideWithValue(api),
          sseStreamProvider.overrideWith((_) => events.stream),
        ],
      );
      addTearDown(() async {
        await events.close();
        container.dispose();
      });

      // Repositories owns the SSE refresh dependency. Activate the same two
      // providers that ReposScreen watches.
      container.listen(configNotifierProvider, (_, _) {});
      container.listen(configRefreshProvider, (_, _) {});

      final initial = await container.read(configNotifierProvider.future);
      expect(fetches, 1);
      expect(initial.repoConfigs['acme/new'], isNull);

      // The overridden stream is broadcast; wait until Riverpod has attached
      // the SSE listener so the event cannot be dropped between provider
      // construction and subscription.
      await _waitFor(() => events.hasListener);
      events.add(SseEvent(type: eventType, data: '{"repo":"acme/new"}'));

      await _waitFor(() {
        final refreshed = container.read(configNotifierProvider).value;
        return fetches > 1 &&
            refreshed?.repoConfigs['acme/new']?.isMonitored == true;
      });

      expect(fetches, 2);
      expect(
        container
            .read(configNotifierProvider)
            .requireValue
            .repoConfigs['acme/new']
            ?.isMonitored,
        isTrue,
      );
    });
  }

  test('SSE during initial load refreshes after build completes', () async {
    const initial = AppConfig(
      repoConfigs: {'acme/existing': RepoConfig(prEnabled: true)},
    );
    const refreshed = AppConfig(
      repoConfigs: {
        'acme/existing': RepoConfig(prEnabled: true),
        'acme/discovered': RepoConfig(prEnabled: true),
      },
    );
    final initialResponse = Completer<Map<String, dynamic>>();
    final events = StreamController<SseEvent>.broadcast();
    var fetches = 0;
    final api = MockApiClient();

    when(() => api.fetchConfig()).thenAnswer((_) {
      fetches++;
      return fetches == 1
          ? initialResponse.future
          : Future.value(refreshed.toJson());
    });

    final container = ProviderContainer(
      overrides: [
        apiClientProvider.overrideWithValue(api),
        sseStreamProvider.overrideWith((_) => events.stream),
      ],
    );
    addTearDown(() async {
      await events.close();
      container.dispose();
    });
    container.listen(configRefreshProvider, (_, _) {});
    final initialLoad = container.read(configNotifierProvider.future);
    await _waitFor(() => fetches == 1 && events.hasListener);

    events.add(
      const SseEvent(
        type: 'repo_discovered',
        data: '{"repo":"acme/discovered"}',
      ),
    );
    await _waitFor(() => container.read(configRefreshProvider) == 1);
    await Future<void>.delayed(Duration.zero);
    expect(fetches, 1, reason: 'refresh must wait for the initial fetch');

    initialResponse.complete(initial.toJson());
    await initialLoad;
    await _waitFor(() {
      return fetches == 2 &&
          container
                  .read(configNotifierProvider)
                  .value
                  ?.repoConfigs['acme/discovered']
                  ?.isMonitored ==
              true;
    });
  });

  test('overlapping saves are serialized without stale UI rollback', () async {
    const initial = AppConfig(pollInterval: '1m');
    final firstPatch = Completer<Map<String, dynamic>>();
    final secondPatch = Completer<Map<String, dynamic>>();
    final patches = <Map<String, dynamic>>[];
    final api = MockApiClient();

    when(() => api.fetchConfig()).thenAnswer((_) async => initial.toJson());
    when(() => api.patchConfig(any())).thenAnswer((invocation) {
      patches.add(
        invocation.positionalArguments.single as Map<String, dynamic>,
      );
      return patches.length == 1 ? firstPatch.future : secondPatch.future;
    });

    final container = ProviderContainer(
      overrides: [apiClientProvider.overrideWithValue(api)],
    );
    addTearDown(container.dispose);
    await container.read(configNotifierProvider.future);

    final notifier = container.read(configNotifierProvider.notifier);
    final first = initial.copyWith(pollInterval: '2m');
    final second = initial.copyWith(pollInterval: '3m');
    final firstSave = notifier.save(first);
    await _waitFor(() => patches.length == 1);
    final secondSave = notifier.save(second);

    expect(
      container.read(configNotifierProvider).requireValue.pollInterval,
      '3m',
    );
    expect(patches, hasLength(1));

    firstPatch.complete(first.toJson());
    await firstSave;
    await _waitFor(() => patches.length == 2);

    // The first response is authoritative for the next diff, but must not
    // overwrite the newer optimistic edit still visible in the UI.
    expect(
      container.read(configNotifierProvider).requireValue.pollInterval,
      '3m',
    );
    expect(patches[0]['github']['poll_interval'], '2m');
    expect(patches[1]['github']['poll_interval'], '3m');

    secondPatch.complete(second.toJson());
    await secondSave;
    expect(
      container.read(configNotifierProvider).requireValue.pollInterval,
      '3m',
    );
  });

  test(
    'later save reapplies earlier intent after failure and keeps discovery',
    () async {
      const initial = AppConfig(
        pollInterval: '1m',
        retentionDays: 30,
        repoConfigs: {'acme/existing': RepoConfig(prEnabled: true)},
      );
      const refreshed = AppConfig(
        pollInterval: '1m',
        retentionDays: 30,
        repoConfigs: {
          'acme/existing': RepoConfig(prEnabled: true),
          'acme/discovered': RepoConfig(prEnabled: true),
        },
      );
      final refreshResponse = Completer<Map<String, dynamic>>();
      final firstPatch = Completer<Map<String, dynamic>>();
      final secondPatch = Completer<Map<String, dynamic>>();
      final patches = <Map<String, dynamic>>[];
      var fetches = 0;
      final api = MockApiClient();

      when(() => api.fetchConfig()).thenAnswer((_) {
        fetches++;
        return fetches == 1
            ? Future.value(initial.toJson())
            : refreshResponse.future;
      });
      when(() => api.patchConfig(any())).thenAnswer((invocation) {
        patches.add(
          invocation.positionalArguments.single as Map<String, dynamic>,
        );
        return patches.length == 1 ? firstPatch.future : secondPatch.future;
      });

      final container = ProviderContainer(
        overrides: [apiClientProvider.overrideWithValue(api)],
      );
      addTearDown(container.dispose);
      await container.read(configNotifierProvider.future);

      final notifier = container.read(configNotifierProvider.notifier);
      final saveA = notifier.save(initial.copyWith(pollInterval: '2m'));
      await _waitFor(() => patches.length == 1);
      final refresh = notifier.refresh();
      final optimisticA = container.read(configNotifierProvider).requireValue;
      final saveB = notifier.save(optimisticA.copyWith(retentionDays: 60));

      final firstFailure = expectLater(saveA, throwsA(isA<StateError>()));
      firstPatch.completeError(StateError('first patch failed'));
      await firstFailure;
      await _waitFor(() => fetches == 2);

      refreshResponse.complete(refreshed.toJson());
      await refresh;
      await _waitFor(() => patches.length == 2);

      final retry = patches[1];
      final github = retry['github'] as Map<String, dynamic>;
      expect(github['poll_interval'], '2m');
      expect(github, isNot(contains('repositories')));
      expect(github, isNot(contains('non_monitored')));
      expect((retry['retention'] as Map<String, dynamic>)['max_days'], 60);

      secondPatch.complete(
        refreshed.copyWith(pollInterval: '2m', retentionDays: 60).toJson(),
      );
      await saveB;
      final saved = container.read(configNotifierProvider).requireValue;
      expect(saved.pollInterval, '2m');
      expect(saved.retentionDays, 60);
      expect(saved.repoConfigs['acme/discovered']?.isMonitored, isTrue);
    },
  );

  test('queued refresh cannot overwrite a newer optimistic save', () async {
    const initial = AppConfig(pollInterval: '1m');
    final refreshed = initial.copyWith(pollInterval: '2m');
    final updated = initial.copyWith(pollInterval: '3m');
    final refreshResponse = Completer<Map<String, dynamic>>();
    final patchResponse = Completer<Map<String, dynamic>>();
    final patches = <Map<String, dynamic>>[];
    var fetches = 0;
    final api = MockApiClient();

    when(() => api.fetchConfig()).thenAnswer((_) {
      fetches++;
      return fetches == 1
          ? Future.value(initial.toJson())
          : refreshResponse.future;
    });
    when(() => api.patchConfig(any())).thenAnswer((invocation) {
      patches.add(
        invocation.positionalArguments.single as Map<String, dynamic>,
      );
      return patchResponse.future;
    });

    final container = ProviderContainer(
      overrides: [apiClientProvider.overrideWithValue(api)],
    );
    addTearDown(container.dispose);
    await container.read(configNotifierProvider.future);

    final notifier = container.read(configNotifierProvider.notifier);
    final refresh = notifier.refresh();
    await _waitFor(() => fetches == 2);
    final save = notifier.save(updated);

    expect(
      container.read(configNotifierProvider).requireValue.pollInterval,
      '3m',
    );
    expect(patches, isEmpty);

    refreshResponse.complete(refreshed.toJson());
    await refresh;
    await _waitFor(() => patches.length == 1);

    expect(
      container.read(configNotifierProvider).requireValue.pollInterval,
      '3m',
    );
    expect(patches.single['github']['poll_interval'], '3m');

    patchResponse.complete(updated.toJson());
    await save;
    expect(
      container.read(configNotifierProvider).requireValue.pollInterval,
      '3m',
    );
  });

  test('SSE discovery survives a scalar save queued behind refresh', () async {
    const initial = AppConfig(
      pollInterval: '1m',
      repoConfigs: {'acme/existing': RepoConfig(prEnabled: true)},
    );
    const refreshed = AppConfig(
      pollInterval: '1m',
      repoConfigs: {
        'acme/existing': RepoConfig(prEnabled: true),
        'acme/discovered': RepoConfig(prEnabled: true),
      },
    );
    final refreshResponse = Completer<Map<String, dynamic>>();
    final patchResponse = Completer<Map<String, dynamic>>();
    final patches = <Map<String, dynamic>>[];
    final events = StreamController<SseEvent>.broadcast();
    var fetches = 0;
    final api = MockApiClient();

    when(() => api.fetchConfig()).thenAnswer((_) {
      fetches++;
      return fetches == 1
          ? Future.value(initial.toJson())
          : refreshResponse.future;
    });
    when(() => api.patchConfig(any())).thenAnswer((invocation) {
      patches.add(
        invocation.positionalArguments.single as Map<String, dynamic>,
      );
      return patchResponse.future;
    });

    final container = ProviderContainer(
      overrides: [
        apiClientProvider.overrideWithValue(api),
        sseStreamProvider.overrideWith((_) => events.stream),
      ],
    );
    addTearDown(() async {
      await events.close();
      container.dispose();
    });
    container.listen(configNotifierProvider, (_, _) {});
    container.listen(configRefreshProvider, (_, _) {});
    await container.read(configNotifierProvider.future);
    await _waitFor(() => events.hasListener);

    events.add(
      const SseEvent(
        type: 'repo_discovered',
        data: '{"repo":"acme/discovered"}',
      ),
    );
    await _waitFor(() => fetches == 2);
    final current = container.read(configNotifierProvider).requireValue;
    final save = container
        .read(configNotifierProvider.notifier)
        .save(current.copyWith(pollInterval: '3m'));

    refreshResponse.complete(refreshed.toJson());
    await _waitFor(() => patches.length == 1);

    final github = patches.single['github'] as Map<String, dynamic>;
    expect(github['poll_interval'], '3m');
    expect(github, isNot(contains('repositories')));
    expect(github, isNot(contains('non_monitored')));

    patchResponse.complete(refreshed.copyWith(pollInterval: '3m').toJson());
    await save;
    final saved = container.read(configNotifierProvider).requireValue;
    expect(saved.repoConfigs['acme/discovered']?.isMonitored, isTrue);
  });

  test('SSE rename survives a queued edit to another repo', () async {
    final refreshResponse = Completer<Map<String, dynamic>>();
    final patchResponse = Completer<Map<String, dynamic>>();
    final patches = <Map<String, dynamic>>[];
    final events = StreamController<SseEvent>.broadcast();
    var fetches = 0;
    final api = MockApiClient();

    when(() => api.fetchConfig()).thenAnswer((_) {
      fetches++;
      return fetches == 1
          ? Future.value({
              'repositories': <String>['acme/old', 'acme/other'],
            })
          : refreshResponse.future;
    });
    when(() => api.patchConfig(any())).thenAnswer((invocation) {
      patches.add(
        invocation.positionalArguments.single as Map<String, dynamic>,
      );
      return patchResponse.future;
    });

    final container = ProviderContainer(
      overrides: [
        apiClientProvider.overrideWithValue(api),
        sseStreamProvider.overrideWith((_) => events.stream),
      ],
    );
    addTearDown(() async {
      await events.close();
      container.dispose();
    });
    container.listen(configNotifierProvider, (_, _) {});
    container.listen(configRefreshProvider, (_, _) {});
    await container.read(configNotifierProvider.future);
    await _waitFor(() => events.hasListener);

    events.add(
      const SseEvent(type: 'repo_renamed', data: '{"repo":"acme/new"}'),
    );
    await _waitFor(() => fetches == 2);
    final current = container.read(configNotifierProvider).requireValue;
    final editedRepos = Map<String, RepoConfig>.from(current.repoConfigs);
    editedRepos['acme/other'] = const RepoConfig(prEnabled: false);
    final save = container
        .read(configNotifierProvider.notifier)
        .save(current.copyWith(repoConfigs: editedRepos));

    refreshResponse.complete({
      'repositories': <String>['acme/new', 'acme/other'],
    });
    await _waitFor(() => patches.length == 1);

    final github = patches.single['github'] as Map<String, dynamic>;
    expect(github['repositories'], <String>['acme/new']);
    expect(github['non_monitored'], <String>['acme/other']);

    patchResponse.complete({
      'repositories': <String>['acme/new'],
      'non_monitored': <String>['acme/other'],
    });
    await save;
    final saved = container.read(configNotifierProvider).requireValue;
    expect(saved.repoConfigs['acme/old'], isNull);
    expect(saved.repoConfigs['acme/new']?.isMonitored, isTrue);
    expect(saved.repoConfigs['acme/other']?.isMonitored, isFalse);
  });

  test(
    'a failed save propagates without poisoning the operation queue',
    () async {
      const initial = AppConfig(pollInterval: '1m');
      final first = initial.copyWith(pollInterval: '2m');
      final second = initial.copyWith(pollInterval: '3m');
      var patches = 0;
      final api = MockApiClient();

      when(() => api.fetchConfig()).thenAnswer((_) async => initial.toJson());
      when(() => api.patchConfig(any())).thenAnswer((_) async {
        patches++;
        if (patches == 1) throw StateError('patch failed');
        return second.toJson();
      });

      final container = ProviderContainer(
        overrides: [apiClientProvider.overrideWithValue(api)],
      );
      addTearDown(container.dispose);
      await container.read(configNotifierProvider.future);

      final notifier = container.read(configNotifierProvider.notifier);
      await expectLater(notifier.save(first), throwsA(isA<StateError>()));
      await notifier.save(second);

      expect(patches, 2);
      expect(
        container.read(configNotifierProvider).requireValue.pollInterval,
        '3m',
      );
    },
  );
}

Future<void> _waitFor(bool Function() condition) async {
  final deadline = DateTime.now().add(const Duration(seconds: 2));
  while (DateTime.now().isBefore(deadline)) {
    if (condition()) return;
    await Future<void>.delayed(const Duration(milliseconds: 10));
  }
  fail('condition was not met before timeout');
}
