import 'dart:io';
import '../models/config_model.dart';
import 'gh_cli.dart';

/// Handles first-run setup: writes config file to disk and stores
/// the GitHub token in macOS Keychain via the `security` CLI.
class FirstRunSetup {
  static const _keychainService = 'heimdallr';
  static const _keychainAccount = 'github-token';

  // ── Token ────────────────────────────────────────────────────────────────

  /// Tries to get a GitHub token in this priority order:
  ///   1. `gh auth token` (gh CLI, no user interaction needed)
  ///   2. Platform credential store (Keychain on macOS, secret-tool/file on Linux)
  ///   3. GITHUB_TOKEN env var
  ///   4. null (user must enter manually)
  static Future<String?> detectToken() async {
    // 1. gh CLI
    final ghToken = await _tokenFromGhCli();
    if (ghToken != null) return ghToken;

    // 2. Platform credential store
    final stored = await getToken();
    if (stored != null) return stored;

    // 3. Env var
    final envToken = Platform.environment['GITHUB_TOKEN'];
    if (envToken != null && envToken.isNotEmpty) return envToken;

    return null;
  }

  static Future<String?> _tokenFromGhCli() => GhCli.authToken();

  /// Stores the GitHub token in the platform credential store.
  /// macOS: Keychain via `security` CLI.
  /// Linux: GNOME/KDE secret service via `secret-tool`; falls back to
  ///        `~/.config/heimdallr/.token` (chmod 600) when secret-tool is unavailable.
  static Future<void> storeToken(String token) async {
    if (Platform.isMacOS) {
      await _storeTokenMacOS(token);
    } else if (Platform.isLinux) {
      await _storeTokenLinux(token);
    }
  }

  /// Retrieves the GitHub token from the platform credential store.
  /// Returns null if not found.
  static Future<String?> getToken() async {
    if (Platform.isMacOS) return _getTokenMacOS();
    if (Platform.isLinux) return _getTokenLinux();
    return null;
  }

  // ── macOS Keychain ───────────────────────────────────────────────────────

  static Future<void> _storeTokenMacOS(String token) async {
    await Process.run('security', [
      'delete-generic-password', '-s', _keychainService, '-a', _keychainAccount,
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

  static Future<String?> _getTokenMacOS() async {
    final result = await Process.run('security', [
      'find-generic-password', '-s', _keychainService, '-a', _keychainAccount, '-w',
    ]);
    if (result.exitCode == 0) {
      final token = (result.stdout as String).trim();
      return token.isEmpty ? null : token;
    }
    return null;
  }

  // ── Linux: secret-tool (GNOME Keyring / KDE Wallet) + file fallback ─────

  static Future<void> _storeTokenLinux(String token) async {
    // Try secret-tool first (requires libsecret + a keyring daemon running)
    try {
      final proc = await Process.start('secret-tool', [
        'store',
        '--label=Heimdallr GitHub Token',
        'service', _keychainService,
        'account', _keychainAccount,
      ]);
      // secret-tool reads the secret from stdin
      proc.stdin.write(token);
      await proc.stdin.close();
      if (await proc.exitCode == 0) return;
    } catch (_) {
      // secret-tool not available — fall through to file fallback
    }
    // Fallback: plain text file with chmod 600
    await _writeTokenFile(token);
  }

  static Future<String?> _getTokenLinux() async {
    // Try secret-tool first
    try {
      final result = await Process.run('secret-tool', [
        'lookup',
        'service', _keychainService,
        'account', _keychainAccount,
      ]);
      if (result.exitCode == 0) {
        final token = (result.stdout as String).trim();
        if (token.isNotEmpty) return token;
      }
    } catch (_) {
      // secret-tool not available
    }
    // Fallback: read from file
    return _readTokenFile();
  }

  // ── Linux file fallback (~/.config/heimdallr/.token, chmod 600) ──────────

  static String _tokenFilePath() {
    final home = Platform.environment['HOME'] ?? '';
    return '$home/.config/heimdallr/.token';
  }

  static Future<void> _writeTokenFile(String token) async {
    final path = _tokenFilePath();
    await Directory(path).parent.create(recursive: true);
    await File(path).writeAsString(token);
    await Process.run('chmod', ['600', path]);
  }

  static Future<String?> _readTokenFile() async {
    final file = File(_tokenFilePath());
    if (!await file.exists()) return null;
    final token = (await file.readAsString()).trim();
    return token.isEmpty ? null : token;
  }

  // ── Config file ──────────────────────────────────────────────────────────

  /// Writes the daemon config file to ~/.config/heimdallr/config.toml
  static Future<void> writeConfig(AppConfig config) async {
    final home = Platform.environment['HOME'] ?? '';
    if (home.isEmpty) throw Exception('HOME environment variable not set');

    final dir = Directory('$home/.config/heimdallr');
    await dir.create(recursive: true);

    final content = _buildToml(config);
    await File('$home/.config/heimdallr/config.toml').writeAsString(content);
  }

  static String _buildToml(AppConfig config) {
    final buf = StringBuffer();

    buf.writeln('[server]');
    buf.writeln('port = ${config.serverPort}');
    buf.writeln();

    buf.writeln('[github]');
    buf.writeln('poll_interval = "${config.pollInterval}"');
    final repos = config.repositories.map((r) => '"$r"').join(', ');
    buf.writeln('repositories = [$repos]');
    buf.writeln();

    buf.writeln('[ai]');
    buf.writeln('primary = "${config.aiPrimary}"');
    if (config.aiFallback.isNotEmpty) {
      buf.writeln('fallback = "${config.aiFallback}"');
    }
    buf.writeln('review_mode = "${config.reviewMode}"');
    buf.writeln();

    // Per-repo overrides (AI + prompt + review mode)
    for (final entry in config.repoConfigs.entries) {
      final repo = entry.key;
      final rc = entry.value;
      if (rc.hasAiOverride) {
        buf.writeln('[ai.repos."$repo"]');
        if (rc.aiPrimary != null) buf.writeln('primary = "${rc.aiPrimary}"');
        if (rc.aiFallback != null) buf.writeln('fallback = "${rc.aiFallback}"');
        if (rc.promptId != null) buf.writeln('prompt = "${rc.promptId}"');
        if (rc.reviewMode != null) buf.writeln('review_mode = "${rc.reviewMode}"');
        buf.writeln();
      }
    }

    buf.writeln('[retention]');
    buf.writeln('max_days = ${config.retentionDays}');

    return buf.toString();
  }

  /// Returns true if a config file already exists.
  static Future<bool> configExists() async {
    final home = Platform.environment['HOME'] ?? '';
    return File('$home/.config/heimdallr/config.toml').exists();
  }
}
