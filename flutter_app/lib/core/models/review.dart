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

  /// GitHub's id for the posted review. 0 while it waits to be published;
  /// negative values are the daemon's terminal "never posted" sentinels
  /// (see [publication]). `published_at` is deliberately not used for this: a
  /// Go zero time serialises as year 0001, not null.
  @JsonKey(name: 'github_review_id', defaultValue: 0)
  final int githubReviewId;

  /// GitHub's state for the posted review (APPROVED, CHANGES_REQUESTED,
  /// COMMENTED, DISMISSED); empty until published.
  @JsonKey(name: 'github_review_state', defaultValue: '')
  final String githubReviewState;

  /// The event the daemon decided to submit (APPROVE, REQUEST_CHANGES,
  /// COMMENT); empty on reviews stored before it was recorded.
  @JsonKey(defaultValue: '')
  final String event;

  /// The PR head commit the review judged; empty on old reviews.
  @JsonKey(name: 'head_sha', defaultValue: '')
  final String headSha;

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
    this.githubReviewId = 0,
    this.githubReviewState = '',
    this.event = '',
    this.headSha = '',
  });

  bool get hasTokenUsage => inputTokens > 0 || outputTokens > 0;

  /// Whether this review reached GitHub, from the daemon's review id sentinels.
  ReviewPublication get publication {
    if (githubReviewId > 0) return ReviewPublication.posted;
    if (githubReviewId == 0) return ReviewPublication.pending;
    if (githubReviewId == -2) return ReviewPublication.superseded;
    return ReviewPublication.abandoned;
  }

  /// A row recording that a peer Heimdallm instance reviewed this commit.
  bool get isPeer => cliUsed == 'peer';

  /// The head commit as GitHub abbreviates it, or empty when unknown.
  String get shortSha => headSha.length > 7 ? headSha.substring(0, 7) : headSha;

  /// The verdict GitHub shows, falling back to the decided event for a review
  /// that is not posted yet. Null when neither is known.
  ReviewVerdict? get verdict {
    switch ((githubReviewState.isNotEmpty ? githubReviewState : event)
        .toUpperCase()) {
      case 'APPROVED':
      case 'APPROVE':
        return ReviewVerdict.approved;
      case 'CHANGES_REQUESTED':
      case 'REQUEST_CHANGES':
        return ReviewVerdict.changesRequested;
      case 'COMMENTED':
      case 'COMMENT':
        return ReviewVerdict.commented;
      case 'DISMISSED':
        return ReviewVerdict.dismissed;
      default:
        return null;
    }
  }

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

/// Whether a stored review reached GitHub. The daemon encodes it in
/// `github_review_id`: a positive id once posted, 0 while a publish retry is
/// still due, -2 when a newer commit superseded it before it was posted, and
/// -1 (or any other negative value) when it will never be posted: the PR was
/// closed or merged first, or GitHub rejected the publish for good.
enum ReviewPublication { posted, pending, abandoned, superseded }

/// The review event as GitHub shows it.
enum ReviewVerdict { approved, changesRequested, commented, dismissed }

/// 950 → "950", 12345 → "12.3k", 4200000 → "4.2M".
String compactTokenCount(num n) {
  if (n >= 1000000) return '${(n / 1000000).toStringAsFixed(1)}M';
  if (n >= 1000) return '${(n / 1000).toStringAsFixed(1)}k';
  return n.round().toString();
}
