/// Known activity actions from the daemon's activity_log. `unknown` is a
/// safety fallback so the UI never crashes on a forward-compat value.
enum ActivityAction { review, triage, implement, promote, error, unknown }

ActivityAction _parseAction(String s) {
  switch (s) {
    case 'review':
      return ActivityAction.review;
    case 'triage':
      return ActivityAction.triage;
    case 'implement':
      return ActivityAction.implement;
    case 'promote':
      return ActivityAction.promote;
    case 'error':
      return ActivityAction.error;
    default:
      return ActivityAction.unknown;
  }
}

class ActivityEntry {
  final int id;
  final DateTime timestamp;
  final String org;
  final String repo;
  final String itemType; // 'pr' | 'issue'
  final int itemNumber;
  final String itemTitle;
  final ActivityAction action;
  final String outcome;
  final Map<String, dynamic> details;

  const ActivityEntry({
    required this.id,
    required this.timestamp,
    required this.org,
    required this.repo,
    required this.itemType,
    required this.itemNumber,
    required this.itemTitle,
    required this.action,
    required this.outcome,
    required this.details,
  });

  factory ActivityEntry.fromJson(Map<String, dynamic> json) {
    return ActivityEntry(
      id: json['id'] as int,
      timestamp: DateTime.parse(json['ts'] as String).toLocal(),
      org: json['org'] as String,
      repo: json['repo'] as String,
      itemType: json['item_type'] as String,
      itemNumber: json['item_number'] as int,
      itemTitle: json['item_title'] as String,
      action: _parseAction(json['action'] as String),
      outcome: (json['outcome'] as String?) ?? '',
      details: (json['details'] as Map?)?.cast<String, dynamic>() ?? {},
    );
  }
}

/// Query used by the providers and API layer. All fields optional.
class ActivityQuery {
  final DateTime? date;
  final DateTime? from;
  final DateTime? to;
  final Set<String> orgs;
  final Set<String> repos;
  final Set<ActivityAction> actions;
  final int limit;

  const ActivityQuery({
    this.date,
    this.from,
    this.to,
    this.orgs = const {},
    this.repos = const {},
    this.actions = const {},
    this.limit = 500,
  });

  Map<String, List<String>> toQueryParameters() {
    String ymd(DateTime d) =>
        '${d.year.toString().padLeft(4, '0')}-${d.month.toString().padLeft(2, '0')}-${d.day.toString().padLeft(2, '0')}';
    final params = <String, List<String>>{};
    if (date != null) {
      params['date'] = [ymd(date!)];
    } else if (from != null && to != null) {
      params['from'] = [ymd(from!)];
      params['to'] = [ymd(to!)];
    }
    if (orgs.isNotEmpty) params['org'] = orgs.toList();
    if (repos.isNotEmpty) params['repo'] = repos.toList();
    if (actions.isNotEmpty) {
      params['action'] = actions.map((a) => a.name).toList();
    }
    params['limit'] = [limit.toString()];
    return params;
  }
}

class ActivityPage {
  final List<ActivityEntry> entries;
  final bool truncated;
  final int count;

  const ActivityPage({
    required this.entries,
    required this.truncated,
    required this.count,
  });

  factory ActivityPage.fromJson(Map<String, dynamic> json) {
    final list = (json['entries'] as List? ?? [])
        .map((e) => ActivityEntry.fromJson(e as Map<String, dynamic>))
        .toList();
    return ActivityPage(
      entries: list,
      truncated: json['truncated'] as bool? ?? false,
      count: json['count'] as int? ?? list.length,
    );
  }
}

/// Thrown by the API client when the daemon returns 503 for /activity.
class ActivityDisabledException implements Exception {
  @override
  String toString() => 'Activity log is disabled in daemon config';
}
