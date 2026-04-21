import 'dart:convert';
import 'package:http/http.dart' as http;

/// Discovers GitHub repos from the user's open PRs
/// (review-requested, assignee, or author).
///
/// This mirrors exactly what the daemon polls, so the repos
/// discovered here are the ones that matter.
///
/// Web-safe: no dart:io imports. Desktop-only gh CLI logic lives in
/// [DesktopRepoDiscovery] and is injected via the [localSearch] callback.
class RepoDiscovery {
  static const _searchQuery =
      'is:pr is:open (review-requested:@me OR assignee:@me OR author:@me)';

  /// Returns repos as "org/repo" strings, sorted alphabetically.
  /// If [localSearch] is provided (desktop only — wraps gh CLI),
  /// try it first and fall through to the HTTP Search API on null/empty.
  static Future<List<String>> discoverFromPRs(
    String token, {
    Future<List<String>?> Function()? localSearch,
  }) async {
    if (localSearch != null) {
      final repos = await localSearch();
      if (repos != null && repos.isNotEmpty) return repos;
    }
    return viaApi(token);
  }

  /// Uses GitHub Search API to list repos from the user's open PRs.
  /// Web-safe — pure `package:http`.
  static Future<List<String>> viaApi(String token) async {
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
