import 'dart:io';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/models/config_model.dart';
import '../../core/setup/first_run_setup.dart';
import '../dashboard/dashboard_providers.dart';

/// Whether the daemon is currently reachable.
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
      // Daemon not running — return defaults so the setup form can display
      return const AppConfig();
    }
  }

  /// Save config via daemon API (daemon already running).
  /// Uses PUT /config for the keys the daemon supports, then reloads.
  /// Does NOT rewrite the TOML file — that preserves hand-edited config
  /// and fields the UI doesn't manage.
  Future<void> save(AppConfig config) async {
    final api = ref.read(apiClientProvider);
    await api.updateConfig(config.toJson());
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
      // 1. Store token in Keychain
      await FirstRunSetup.storeToken(token);
      // Invalidate the cached token so the ApiClient re-reads it on the next request.
      ref.read(apiClientProvider).clearTokenCache();

      // 2. Write config to ~/.config/heimdallm/config.toml
      await FirstRunSetup.writeConfig(config);

      // 3. Launch daemon
      await Process.start(daemonBinaryPath, []);

      // 4. Wait up to 8 seconds for daemon to become healthy
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

      // 5. Refresh health provider
      ref.invalidate(daemonHealthProvider);
      return config;
    });
  }
}

final configNotifierProvider = AsyncNotifierProvider<ConfigNotifier, AppConfig>(
  ConfigNotifier.new,
);
