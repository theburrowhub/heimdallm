import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/models/config_model.dart';
import '../../core/platform/platform_services_provider.dart';
import '../dashboard/dashboard_providers.dart';

final daemonHealthProvider = FutureProvider<bool>((ref) async {
  final api = ref.watch(apiClientProvider);
  return api.checkHealth();
});

final configProvider = FutureProvider<AppConfig>((ref) async {
  final api = ref.watch(apiClientProvider);
  try {
    final json = await api.fetchConfig();
    return AppConfig.fromJson(json);
  } catch (_) {
    return const AppConfig();
  }
});

class ConfigNotifier extends AsyncNotifier<AppConfig> {
  @override
  Future<AppConfig> build() async {
    final api = ref.watch(apiClientProvider);
    try {
      final json = await api.fetchConfig();
      return AppConfig.fromJson(json);
    } catch (_) {
      return const AppConfig();
    }
  }

  /// Save config: write to TOML + tell daemon to reload.
  /// The TOML file is the single source of truth for per-repo overrides
  /// (the daemon's PUT /config only supports a subset of global keys).
  Future<void> save(AppConfig config) async {
    await ref.read(platformServicesProvider).writeDaemonConfig(config);
    final api = ref.read(apiClientProvider);
    await api.reloadConfig();
    state = AsyncValue.data(config);
  }

  /// First-run setup: write config file to disk, store token in Keychain,
  /// then launch the daemon binary and wait for it to become healthy.
  Future<void> saveAndStartDaemon({
    required String token,
    required AppConfig config,
    required String daemonBinaryPath,
  }) async {
    state = const AsyncLoading();
    state = await AsyncValue.guard(() async {
      final platform = ref.read(platformServicesProvider);
      // 1. Store token
      await platform.storeGitHubToken(token);
      // Invalidate the cached token so the ApiClient re-reads it.
      ref.read(apiClientProvider).clearTokenCache();

      // 2. Write config
      await platform.writeDaemonConfig(config);

      // 3. Launch daemon
      await platform.spawnDaemon(daemonBinaryPath);

      // 4. Wait up to 8 seconds for the daemon to become healthy
      final api = ref.read(apiClientProvider);
      for (var i = 0; i < 80; i++) {
        await Future.delayed(const Duration(milliseconds: 100));
        if (await api.checkHealth()) break;
      }
      if (!await api.checkHealth()) {
        throw Exception(
          'Heimdallm could not start. Check the app installation.',
        );
      }
      ref.invalidate(daemonHealthProvider);
      return config;
    });
  }
}

final configNotifierProvider =
    AsyncNotifierProvider<ConfigNotifier, AppConfig>(ConfigNotifier.new);
