import 'dart:convert';
import 'gh_cli.dart';

/// Desktop-only gh-CLI-based repo discovery. Kept out of the shared
/// RepoDiscovery class so that file stays web-safe (no dart:io import).
class DesktopRepoDiscovery {
  /// Returns repos as "org/repo" strings via the gh CLI, or null if
  /// gh isn't installed / reachable.
  static Future<List<String>?> viaGhCli() async {
    final ghPath = await GhCli.findPath();
    if (ghPath == null) return null;
    final all = <String>{};
    final r1 = await GhCli.searchPRs(['--review-requested=@me', '--limit', '200', '--json', 'repository']);
    if (r1 != null) all.addAll(_parseGhSearchOutput(r1));
    final r2 = await GhCli.searchPRs(['--assignee=@me', '--limit', '200', '--json', 'repository']);
    if (r2 != null) all.addAll(_parseGhSearchOutput(r2));
    final r3 = await GhCli.searchPRs(['--author=@me', '--state=open', '--limit', '200', '--json', 'repository']);
    if (r3 != null) all.addAll(_parseGhSearchOutput(r3));
    if (all.isEmpty) return null;
    return all.toList()..sort();
  }

  static List<String> _parseGhSearchOutput(String output) {
    try {
      final list = jsonDecode(output) as List<dynamic>;
      final repos = <String>{};
      for (final item in list) {
        final repo = item['repository'];
        if (repo is Map) {
          final name = repo['nameWithOwner'] as String?;
          if (name != null) repos.add(name);
        }
      }
      return repos.toList()..sort();
    } catch (_) {
      return [];
    }
  }
}
