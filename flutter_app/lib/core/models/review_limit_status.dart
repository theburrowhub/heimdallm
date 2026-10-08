/// Live usage of one review budget window (GET /review-limits).
class ReviewLimitWindowUsage {
  final String window; // minute | hour | day
  final int used;
  final int limit;

  /// When the oldest counted review leaves the window (null = none counted).
  final DateTime? resetAt;

  const ReviewLimitWindowUsage({
    required this.window,
    required this.used,
    required this.limit,
    this.resetAt,
  });

  bool get exhausted => limit > 0 && used >= limit;

  factory ReviewLimitWindowUsage.fromJson(Map<String, dynamic> json) {
    final reset = json['reset_at'] as String?;
    final parsed = reset == null ? null : DateTime.tryParse(reset);
    return ReviewLimitWindowUsage(
      window: (json['window'] as String?) ?? '',
      used: (json['used'] as num?)?.toInt() ?? 0,
      limit: (json['limit'] as num?)?.toInt() ?? 0,
      // Go serialises a zero time.Time as 0001-01-01; treat it as unset.
      resetAt: parsed == null || parsed.year <= 1 ? null : parsed,
    );
  }
}

/// Live usage of one review budget: global, an org, a repo or an agent.
class ReviewLimitStatus {
  final String kind; // global | org | repo | agent
  final String key;
  final List<ReviewLimitWindowUsage> windows;

  const ReviewLimitStatus({
    required this.kind,
    required this.key,
    required this.windows,
  });

  String get label => switch (kind) {
    'global' => 'All reviews',
    'org' => 'Org $key',
    'repo' => key,
    'agent' => 'Agent $key',
    _ => key.isEmpty ? kind : '$kind $key',
  };

  bool get exhausted => windows.any((w) => w.exhausted);

  factory ReviewLimitStatus.fromJson(Map<String, dynamic> json) =>
      ReviewLimitStatus(
        kind: (json['kind'] as String?) ?? '',
        key: (json['key'] as String?) ?? '',
        windows: ((json['windows'] as List<dynamic>?) ?? const [])
            .whereType<Map<String, dynamic>>()
            .map(ReviewLimitWindowUsage.fromJson)
            .toList(),
      );
}
