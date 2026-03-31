import 'dart:convert';
import 'package:http/http.dart' as http;
import 'gh_cli.dart';

/// Discovers GitHub repos from the user's open PRs
/// (review-requested, assignee, or author).
///
/// This mirrors exactly what the daemon polls, so the repos
/// discovered here are the ones that matter.
class RepoDiscovery {
  static const _searchQuery =
      'is:pr is:open (review-requested:@me OR assignee:@me OR author:@me)';

  /// Returns repos as "org/repo" strings, sorted alphabetically.
  /// Tries gh CLI first, falls back to GitHub API.
  static Future<List<String>> discoverFromPRs(String token) async {
    // Try gh CLI first (uses GhCli which handles Homebrew PATH)
    final ghPath = await GhCli.findPath();
    if (ghPath != null) {
      final repos = await _viaGhCli(ghPath);
      if (repos.isNotEmpty) return repos;
    }

    // Fall back to GitHub Search API
    return _viaApi(token);
  }

  static Future<List<String>> _viaGhCli(String ghPath) async {
    final all = <String>{};
    // review-requested
    final r1 = await GhCli.searchPRs(['--review-requested=@me', '--limit', '200', '--json', 'repository']);
    if (r1 != null) all.addAll(_parseGhSearchOutput(r1));
    // assignee
    final r2 = await GhCli.searchPRs(['--assignee=@me', '--limit', '200', '--json', 'repository']);
    if (r2 != null) all.addAll(_parseGhSearchOutput(r2));
    // authored (state:open is default)
    final r3 = await GhCli.searchPRs(['--author=@me', '--state=open', '--limit', '200', '--json', 'repository']);
    if (r3 != null) all.addAll(_parseGhSearchOutput(r3));
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

  // Uses GitHub Search API — extracts repo from repository_url field
  static Future<List<String>> _viaApi(String token) async {
    final repos = <String>{};
    final client = http.Client();
    try {
      // Paginate up to 3 pages (300 PRs should be more than enough)
      for (var page = 1; page <= 3; page++) {
        final uri = Uri.https('api.github.com', '/search/issues', {
          'q': _searchQuery,
          'per_page': '100',
          'page': '$page',
        });
        final resp = await client.get(uri, headers: {
          'Authorization': 'Bearer $token',
          'Accept': 'application/vnd.github+json',
          'X-GitHub-Api-Version': '2022-11-28',
        });
        if (resp.statusCode != 200) break;
        final body = jsonDecode(resp.body) as Map<String, dynamic>;
        final items = body['items'] as List<dynamic>? ?? [];
        if (items.isEmpty) break;

        for (final item in items) {
          // repository_url: "https://api.github.com/repos/org/repo"
          final repoUrl = item['repository_url'] as String?;
          if (repoUrl != null) {
            const prefix = 'https://api.github.com/repos/';
            if (repoUrl.startsWith(prefix)) {
              repos.add(repoUrl.substring(prefix.length));
            }
          }
        }
        if (items.length < 100) break;
      }
    } finally {
      client.close();
    }
    return repos.toList()..sort();
  }

}
