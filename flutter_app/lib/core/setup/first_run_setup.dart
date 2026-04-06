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
    // Write to a temp file first, chmod it, then rename atomically so the
    // token is never world-readable even for a brief window (race condition).
    final tmpPath = '$path.tmp';
    await File(tmpPath).writeAsString(token);
    await Process.run('chmod', ['600', tmpPath]);
    await File(tmpPath).rename(path);
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

  /// Escapes backslashes, double-quotes, and newline characters in a
  /// user-supplied string so it is safe to embed inside a TOML basic string
  /// (i.e. between double-quote delimiters).  Without this, a value such as
  /// `foo"\n[malicious]` could break out of the string context and inject
  /// arbitrary TOML sections.
  static String _tomlEscapeString(String s) => s
      .replaceAll(r'\', r'\\')
      .replaceAll('"', r'\"')
      .replaceAll('\n', r'\n')
      .replaceAll('\r', r'\r');

  static String _buildToml(AppConfig config) {
    final buf = StringBuffer();

    buf.writeln('[server]');
    buf.writeln('port = ${config.serverPort}');
    buf.writeln();

    buf.writeln('[github]');
    buf.writeln('poll_interval = "${_tomlEscapeString(config.pollInterval)}"');
    final repos = config.repositories.map((r) => '"${_tomlEscapeString(r)}"').join(', ');
    buf.writeln('repositories = [$repos]');
    // Persist non-monitored repos so the UI can display and re-enable them after restart.
    final nonMonitored = config.repoConfigs.entries
        .where((e) => !e.value.monitored)
        .map((e) => e.key)
        .toList()..sort();
    if (nonMonitored.isNotEmpty) {
      final nonMon = nonMonitored.map((r) => '"${_tomlEscapeString(r)}"').join(', ');
      buf.writeln('non_monitored = [$nonMon]');
    }
    buf.writeln();

    buf.writeln('[ai]');
    buf.writeln('primary = "${_tomlEscapeString(config.aiPrimary)}"');
    if (config.aiFallback.isNotEmpty) {
      buf.writeln('fallback = "${_tomlEscapeString(config.aiFallback)}"');
    }
    buf.writeln('review_mode = "${_tomlEscapeString(config.reviewMode)}"');
    buf.writeln();

    // Per-agent CLI configs
    for (final entry in config.agentConfigs.entries) {
      final name = entry.key;
      final ac = entry.value;
      if (ac.hasConfig) {
        buf.writeln('[ai.agents.$name]');
        if (ac.model.isNotEmpty) buf.writeln('model = "${_tomlEscapeString(ac.model)}"');
        if (ac.maxTurns > 0) buf.writeln('max_turns = ${ac.maxTurns}');
        if (ac.approvalMode.isNotEmpty) buf.writeln('approval_mode = "${_tomlEscapeString(ac.approvalMode)}"');
        if (ac.extraFlags.isNotEmpty) buf.writeln('extra_flags = "${_tomlEscapeString(ac.extraFlags)}"');
        if (ac.promptId != null) buf.writeln('prompt = "${_tomlEscapeString(ac.promptId!)}"');
        if (ac.effort.isNotEmpty) buf.writeln('effort = "${_tomlEscapeString(ac.effort)}"');
        if (ac.permissionMode.isNotEmpty) buf.writeln('permission_mode = "${_tomlEscapeString(ac.permissionMode)}"');
        if (ac.bare) buf.writeln('bare = true');
        if (ac.dangerouslySkipPerms) buf.writeln('dangerously_skip_perms = true');
        if (ac.noSessionPersistence) buf.writeln('no_session_persistence = true');
        buf.writeln();
      }
    }

    // Per-repo overrides (AI + prompt + review mode + local dir)
    for (final entry in config.repoConfigs.entries) {
      final repo = entry.key;
      final rc = entry.value;
      if (rc.hasAiOverride) {
        buf.writeln('[ai.repos."${_tomlEscapeString(repo)}"]');
        if (rc.aiPrimary != null) buf.writeln('primary = "${_tomlEscapeString(rc.aiPrimary!)}"');
        if (rc.aiFallback != null) buf.writeln('fallback = "${_tomlEscapeString(rc.aiFallback!)}"');
        if (rc.promptId != null) buf.writeln('prompt = "${_tomlEscapeString(rc.promptId!)}"');
        if (rc.reviewMode != null) buf.writeln('review_mode = "${_tomlEscapeString(rc.reviewMode!)}"');
        if (rc.localDir != null && rc.localDir!.isNotEmpty) {
          buf.writeln('local_dir = "${_tomlEscapeString(rc.localDir!)}"');
        }
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
