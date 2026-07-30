import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:mocktail/mocktail.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';

class _MockApiClient extends Mock implements ApiClient {}

Map<String, dynamic> _configJson(bool dangerouslySkipPerms) => {
  ...const AppConfig().toJson(),
  'agent_configs': {
    'claude': {'dangerously_skip_perms': dangerouslySkipPerms},
  },
};

void main() {
  setUpAll(() {
    registerFallbackValue(<String, dynamic>{});
  });

  test('config diff emits a dangerous safety downgrade', () async {
    final api = _MockApiClient();
    Map<String, dynamic>? capturedPatch;
    when(api.fetchConfig).thenAnswer((_) async => _configJson(true));
    when(() => api.patchConfig(any())).thenAnswer((invocation) async {
      capturedPatch = invocation.positionalArguments[0] as Map<String, dynamic>;
      return _configJson(false);
    });

    final container = ProviderContainer(
      overrides: [apiClientProvider.overrideWithValue(api)],
    );
    addTearDown(container.dispose);
    final initial = await container.read(configNotifierProvider.future);

    await container
        .read(configNotifierProvider.notifier)
        .save(
          initial.copyWith(
            agentConfigs: {
              'claude': initial.agentConfigs['claude']!.copyWith(
                dangerouslySkipPerms: false,
              ),
            },
          ),
        );

    expect(
      capturedPatch!['ai']['agents']['claude']['dangerously_skip_perms'],
      isFalse,
    );
  });

  test('config diff rejects a dangerous privilege elevation', () async {
    final api = _MockApiClient();
    when(api.fetchConfig).thenAnswer((_) async => _configJson(false));

    final container = ProviderContainer(
      overrides: [apiClientProvider.overrideWithValue(api)],
    );
    addTearDown(container.dispose);
    final initial = await container.read(configNotifierProvider.future);

    expect(
      () => container
          .read(configNotifierProvider.notifier)
          .save(
            initial.copyWith(
              agentConfigs: {
                'claude': initial.agentConfigs['claude']!.copyWith(
                  dangerouslySkipPerms: true,
                ),
              },
            ),
          ),
      throwsStateError,
    );
    verifyNever(() => api.patchConfig(any()));
  });
}
