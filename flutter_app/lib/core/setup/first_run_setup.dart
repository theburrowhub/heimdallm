import 'dart:io';
import 'package:flutter/foundation.dart' show visibleForTesting;
import 'package:toml/toml.dart';
import '../models/config_model.dart';
import 'gh_cli.dart';

/// Handles first-run setup: writes config file to disk and stores
/// the GitHub token in macOS Keychain via the `security` CLI.
class FirstRunSetup {
  static const _keychainService = 'heimdallm';
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
  ///        `~/.config/heimdallm/.token` (chmod 600) when secret-tool is unavailable.
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
      'delete-generic-password',
      '-s',
      _keychainService,
      '-a',
      _keychainAccount,
    ]);
    final result = await Process.run('security', [
      'add-generic-password',
      '-s',
      _keychainService,
      '-a',
      _keychainAccount,
      '-w',
      token,
    ]);
    if (result.exitCode != 0) {
      throw Exception('Failed to store token in Keychain: ${result.stderr}');
    }
  }

  static Future<String?> _getTokenMacOS() async {
    final result = await Process.run('security', [
      'find-generic-password',
      '-s',
      _keychainService,
      '-a',
      _keychainAccount,
      '-w',
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
        '--label=Heimdallm GitHub Token',
        'service',
        _keychainService,
        'account',
        _keychainAccount,
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
        'service',
        _keychainService,
        'account',
        _keychainAccount,
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

  // ── Linux file fallback (~/.config/heimdallm/.token, chmod 600) ──────────

  static String _tokenFilePath() {
    final home = Platform.environment['HOME'] ?? '';
    return '$home/.config/heimdallm/.token';
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

  /// Writes the daemon config file to ~/.config/heimdallm/config.toml.
  ///
  /// [configPathOverride] exists only for tests — production callers always
  /// use the real path derived from $HOME.
  ///
  /// If a config.toml already exists, this is a MERGE, not an overwrite:
  /// [AppConfig] models only a subset of the daemon's schema ([server]'s
  /// port, [github], [ai], [retention]). Everything else — [cluster] and its
  /// instances/tokens/routing, [polling], [merge_tracking],
  /// [circuit_breaker], [activity_log], server.bind_addr,
  /// github.token, any operator-only key — is preserved from the file on
  /// disk untouched. Before this, "Save and start Heimdallm" (shown
  /// precisely when the daemon is down and GET /config could not populate
  /// the real values) blindly wrote AppConfig's mostly-default view over the
  /// whole file, silently deleting every section it didn't model. That is
  /// what actually emptied config.toml in the incident this fixes.
  static Future<void> writeConfig(
    AppConfig config, {
    @visibleForTesting String? configPathOverride,
  }) async {
    String path;
    if (configPathOverride != null) {
      path = configPathOverride;
    } else {
      final home = Platform.environment['HOME'] ?? '';
      if (home.isEmpty) throw Exception('HOME environment variable not set');
      path = '$home/.config/heimdallm/config.toml';
    }

    await Directory(File(path).parent.path).create(recursive: true);

    final content = await _mergedTomlFor(config, path);
    await _writeConfigAtomically(path, content);
  }

  /// Builds the TOML to write at [path]: AppConfig's own sections deep-merged
  /// over whatever is already on disk there, so keys AppConfig does not model
  /// survive. With no existing file (first run) or an empty one, this is just
  /// AppConfig's own TOML — nothing to merge with.
  static Future<String> _mergedTomlFor(AppConfig config, String path) async {
    final appToml = _buildToml(config);
    final file = File(path);
    if (!await file.exists()) return appToml;

    final existingText = await file.readAsString();
    if (existingText.trim().isEmpty) return appToml;

    final Map<String, dynamic> existingMap;
    try {
      existingMap = TomlDocument.parse(existingText).toMap();
    } catch (e) {
      // A config.toml this app cannot parse (hand-edited, a version skew, an
      // in-progress write from the daemon) must never be silently discarded.
      // The whole point of merging is to never destroy what's on disk — so
      // refuse rather than guess.
      throw Exception(
        'config.toml exists at $path but could not be parsed for a safe '
        'merge; refusing to overwrite it. Fix or remove the file manually, '
        'then try again. ($e)',
      );
    }
    final appMap = TomlDocument.parse(appToml).toMap();
    final merged = _deepMergeToml(existingMap, appMap);
    return TomlDocument.fromMap(merged).toString();
  }

  /// Deep-merges [patch] over [base]: recurses when both sides have a map at
  /// the same key, otherwise [patch]'s value wins. Mirrors the daemon's own
  /// config.DeepMerge (internal/config/writer.go) so both sides of the
  /// save path agree on what "merge" means.
  static Map<String, dynamic> _deepMergeToml(Map base, Map patch) {
    final merged = <String, dynamic>{
      for (final entry in base.entries) entry.key as String: entry.value,
    };
    for (final entry in patch.entries) {
      final key = entry.key as String;
      final patchValue = entry.value;
      final baseValue = merged[key];
      merged[key] = (baseValue is Map && patchValue is Map)
          ? _deepMergeToml(baseValue, patchValue)
          : patchValue;
    }
    return merged;
  }

  /// Writes via temp file + rename so a reader (the daemon, or this same
  /// method racing a future call) never observes a truncated file — the
  /// original `writeAsString` truncated in place first.
  static Future<void> _writeConfigAtomically(String path, String content) async {
    final tmpPath = '$path.tmp.$pid';
    final tmpFile = File(tmpPath);
    try {
      await tmpFile.writeAsString(content, flush: true);
      await tmpFile.rename(path);
    } catch (e) {
      // Best-effort cleanup; the rename failing is the error that matters.
      if (await tmpFile.exists()) {
        await tmpFile.delete().catchError((_) => tmpFile);
      }
      rethrow;
    }
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

  @visibleForTesting
  static String buildTomlForTesting(AppConfig config) => _buildToml(config);

  static String _buildToml(AppConfig config) {
    final buf = StringBuffer();

    buf.writeln('[server]');
    buf.writeln('port = ${config.serverPort}');
    buf.writeln();

    buf.writeln('[github]');
    buf.writeln('poll_interval = "${_tomlEscapeString(config.pollInterval)}"');
    final repos = config.repositories
        .map((r) => '"${_tomlEscapeString(r)}"')
        .join(', ');
    buf.writeln('repositories = [$repos]');
    // Persist non-monitored repos so the UI can display and re-enable them after restart.
    final nonMonitored =
        config.repoConfigs.entries
            .where((e) => !e.value.isMonitored)
            .map((e) => e.key)
            .toList()
          ..sort();
    if (nonMonitored.isNotEmpty) {
      final nonMon = nonMonitored
          .map((r) => '"${_tomlEscapeString(r)}"')
          .join(', ');
      buf.writeln('non_monitored = [$nonMon]');
    }
    // Persist local_dir_base so a Flutter-side save doesn't silently drop
    // the operator's full-repo-analysis base path list. The daemon reads
    // it on startup (TOML) + on each review (ResolveLocalDir).
    if (config.localDirBase.isNotEmpty) {
      final bases = config.localDirBase
          .where((p) => p.isNotEmpty)
          .map((p) => '"${_tomlEscapeString(p)}"')
          .join(', ');
      if (bases.isNotEmpty) {
        buf.writeln('local_dir_base = [$bases]');
      }
    }
    buf.writeln();

    buf.writeln('[ai]');
    buf.writeln('primary = "${_tomlEscapeString(config.aiPrimary)}"');
    if (config.aiFallback.isNotEmpty) {
      buf.writeln('fallback = "${_tomlEscapeString(config.aiFallback)}"');
    }
    buf.writeln('review_mode = "${_tomlEscapeString(config.reviewMode)}"');
    if (config.globalCloneDir.isNotEmpty) {
      buf.writeln('clone_dir = "${_tomlEscapeString(config.globalCloneDir)}"');
    }
    buf.writeln();

    // Per-agent CLI configs
    for (final entry in config.agentConfigs.entries) {
      final name = entry.key;
      final ac = entry.value;
      if (ac.hasConfig) {
        buf.writeln('[ai.agents.$name]');
        if (ac.model.isNotEmpty) {
          buf.writeln('model = "${_tomlEscapeString(ac.model)}"');
        }
        if (ac.maxTurns > 0) buf.writeln('max_turns = ${ac.maxTurns}');
        if (ac.approvalMode.isNotEmpty) {
          buf.writeln(
            'approval_mode = "${_tomlEscapeString(ac.approvalMode)}"',
          );
        }
        if (ac.extraFlags.isNotEmpty) {
          buf.writeln('extra_flags = "${_tomlEscapeString(ac.extraFlags)}"');
        }
        if (ac.promptId != null) {
          buf.writeln('prompt = "${_tomlEscapeString(ac.promptId!)}"');
        }
        if (ac.effort.isNotEmpty) {
          buf.writeln('effort = "${_tomlEscapeString(ac.effort)}"');
        }
        if (ac.permissionMode.isNotEmpty) {
          buf.writeln(
            'permission_mode = "${_tomlEscapeString(ac.permissionMode)}"',
          );
        }
        if (ac.bare) buf.writeln('bare = true');
        if (ac.dangerouslySkipPerms) {
          buf.writeln('dangerously_skip_perms = true');
        }
        if (ac.noSessionPersistence) {
          buf.writeln('no_session_persistence = true');
        }
        buf.writeln();
      }
    }

    // Per-org overrides.
    for (final entry in config.orgConfigs.entries) {
      final org = entry.key;
      final oc = entry.value;
      if (oc.hasOverride) {
        buf.writeln('[ai.orgs."${_tomlEscapeString(org)}"]');
        if (oc.aiPrimary != null) {
          buf.writeln('primary = "${_tomlEscapeString(oc.aiPrimary!)}"');
        }
        if (oc.aiFallback != null) {
          buf.writeln('fallback = "${_tomlEscapeString(oc.aiFallback!)}"');
        }
        if (oc.promptId != null) {
          buf.writeln('prompt = "${_tomlEscapeString(oc.promptId!)}"');
        }
        if (oc.reviewMode != null) {
          buf.writeln('review_mode = "${_tomlEscapeString(oc.reviewMode!)}"');
        }
        if (oc.localDir != null && oc.localDir!.isNotEmpty) {
          buf.writeln('local_dir = "${_tomlEscapeString(oc.localDir!)}"');
        }
        if (oc.cloneDir != null && oc.cloneDir!.isNotEmpty) {
          buf.writeln('clone_dir = "${_tomlEscapeString(oc.cloneDir!)}"');
        }
        buf.writeln();
      }
    }

    // Per-repo overrides (AI + prompt + review mode + local dir)
    for (final entry in config.repoConfigs.entries) {
      final repo = entry.key;
      final rc = entry.value;
      if (rc.hasAiOverride) {
        buf.writeln('[ai.repos."${_tomlEscapeString(repo)}"]');
        if (rc.aiPrimary != null) {
          buf.writeln('primary = "${_tomlEscapeString(rc.aiPrimary!)}"');
        }
        if (rc.aiFallback != null) {
          buf.writeln('fallback = "${_tomlEscapeString(rc.aiFallback!)}"');
        }
        if (rc.promptId != null) {
          buf.writeln('prompt = "${_tomlEscapeString(rc.promptId!)}"');
        }
        if (rc.reviewMode != null) {
          buf.writeln('review_mode = "${_tomlEscapeString(rc.reviewMode!)}"');
        }
        if (rc.localDir != null && rc.localDir!.isNotEmpty) {
          buf.writeln('local_dir = "${_tomlEscapeString(rc.localDir!)}"');
        }
        if (rc.cloneDir != null && rc.cloneDir!.isNotEmpty) {
          buf.writeln('clone_dir = "${_tomlEscapeString(rc.cloneDir!)}"');
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
    return File('$home/.config/heimdallm/config.toml').exists();
  }
}
