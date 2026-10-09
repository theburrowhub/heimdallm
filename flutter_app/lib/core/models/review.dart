import 'package:json_annotation/json_annotation.dart';
import 'issue.dart';
part 'review.g.dart';

@JsonSerializable()
class Review {
  final int id;
  @JsonKey(name: 'pr_id')
  final int prId;
  @JsonKey(name: 'cli_used')
  final String cliUsed;
  final String summary;
  final List<Issue> issues;
  final String severity;
  @JsonKey(name: 'created_at')
  final DateTime createdAt;

  /// Token usage of the run (0 on reviews made before tracking existed).
  @JsonKey(name: 'input_tokens', defaultValue: 0)
  final int inputTokens;
  @JsonKey(name: 'output_tokens', defaultValue: 0)
  final int outputTokens;
  @JsonKey(name: 'cache_read_tokens', defaultValue: 0)
  final int cacheReadTokens;
  @JsonKey(name: 'cost_usd', defaultValue: 0)
  final double costUsd;

  /// True when the agent did not report usage and the daemon estimated it.
  @JsonKey(name: 'tokens_estimated', defaultValue: false)
  final bool tokensEstimated;

  const Review({
    required this.id,
    required this.prId,
    required this.cliUsed,
    required this.summary,
    required this.issues,
    required this.severity,
    required this.createdAt,
    this.inputTokens = 0,
    this.outputTokens = 0,
    this.cacheReadTokens = 0,
    this.costUsd = 0,
    this.tokensEstimated = false,
  });

  bool get hasTokenUsage => inputTokens > 0 || outputTokens > 0;

  /// "12.3k in · 450 out tokens · \$0.02", prefixed with "≈" when estimated.
  String get tokenUsageLabel {
    final parts = <String>[
      '${compactTokenCount(inputTokens)} in',
      '${compactTokenCount(outputTokens)} out tokens',
      if (cacheReadTokens > 0) '${compactTokenCount(cacheReadTokens)} cached',
      if (costUsd > 0) '\$${costUsd.toStringAsFixed(costUsd < 0.01 ? 4 : 2)}',
    ];
    return '${tokensEstimated ? '≈ ' : ''}${parts.join(' · ')}';
  }

  factory Review.fromJson(Map<String, dynamic> json) => _$ReviewFromJson(json);
  Map<String, dynamic> toJson() => _$ReviewToJson(this);
}

/// 950 → "950", 12345 → "12.3k", 4200000 → "4.2M".
String compactTokenCount(num n) {
  if (n >= 1000000) return '${(n / 1000000).toStringAsFixed(1)}M';
  if (n >= 1000) return '${(n / 1000).toStringAsFixed(1)}k';
  return n.round().toString();
}
