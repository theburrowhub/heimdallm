import 'dart:io';
import '../models/config_model.dart';

/// Handles first-run setup: writes config file to disk and stores
/// the GitHub token in macOS Keychain via the `security` CLI.
class FirstRunSetup {
  static const _keychainService = 'heimdallr';
  static const _keychainAccount = 'github-token';

  /// Stores the GitHub token in macOS Keychain.
  static Future<void> storeToken(String token) async {
    // Delete existing entry silently (ignore failure)
    await Process.run('security', [
      'delete-generic-password',
      '-s', _keychainService,
      '-a', _keychainAccount,
    ]);
    final result = await Process.run('security', [
      'add-generic-password',
      '-s', _keychainService,
      '-a', _keychainAccount,
      '-w', token,
    ]);
    if (result.exitCode != 0) {
      throw Exception('Failed to store token in Keychain: ${result.stderr}');
    }
  }

  /// Retrieves the GitHub token from macOS Keychain.
  /// Returns null if not found.
  static Future<String?> getToken() async {
    final result = await Process.run('security', [
      'find-generic-password',
      '-s', _keychainService,
      '-a', _keychainAccount,
      '-w',
    ]);
    if (result.exitCode == 0) {
      final token = (result.stdout as String).trim();
      return token.isEmpty ? null : token;
    }
    return null;
  }

  /// Writes the daemon config file to ~/.config/heimdallr/config.toml
  static Future<void> writeConfig(AppConfig config) async {
    final home = Platform.environment['HOME'] ?? '';
    if (home.isEmpty) throw Exception('HOME environment variable not set');

    final dir = Directory('$home/.config/heimdallr');
    await dir.create(recursive: true);

    final repos = config.repositories.map((r) => '"$r"').join(', ');
    final fallbackLine = config.aiFallback.isNotEmpty
        ? 'fallback = "${config.aiFallback}"\n'
        : '';

    final content = '''[server]
port = ${config.serverPort}

[github]
poll_interval = "${config.pollInterval}"
repositories = [$repos]

[ai]
primary = "${config.aiPrimary}"
${fallbackLine}
[retention]
max_days = ${config.retentionDays}
''';
    final file = File('$home/.config/heimdallr/config.toml');
    await file.writeAsString(content);
  }

  /// Returns true if a config file already exists.
  static Future<bool> configExists() async {
    final home = Platform.environment['HOME'] ?? '';
    return File('$home/.config/heimdallr/config.toml').exists();
  }
}
