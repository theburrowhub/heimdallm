import 'package:json_annotation/json_annotation.dart';
part 'tracked_issue.g.dart';

@JsonSerializable()
class TrackedIssueReview {
  final int id;
  @JsonKey(name: 'issue_id')
  final int issueId;
  @JsonKey(name: 'cli_used')
  final String cliUsed;
  final String summary;
  final Map<String, dynamic> triage;
  final List<dynamic> suggestions;
  @JsonKey(name: 'action_taken')
  final String actionTaken;
  @JsonKey(name: 'pr_created')
  final int prCreated;
  @JsonKey(name: 'created_at')
  final DateTime createdAt;

  const TrackedIssueReview({
    required this.id,
    required this.issueId,
    required this.cliUsed,
    required this.summary,
    required this.triage,
    required this.suggestions,
    required this.actionTaken,
    required this.prCreated,
    required this.createdAt,
  });

  factory TrackedIssueReview.fromJson(Map<String, dynamic> json) =>
      _$TrackedIssueReviewFromJson(json);
  Map<String, dynamic> toJson() => _$TrackedIssueReviewToJson(this);

  String get severity => (triage['severity'] as String?) ?? 'low';
  String get category => (triage['category'] as String?) ?? '';
}

/// TrackedIssueLinkedPR carries the slim PR-side view embedded on the
/// issue response so the dashboard can render the review-state chip
/// (#482 phase 1) without an extra round-trip. Populated only when
/// auto_implement created the PR and the daemon's Tier 3 watch has
/// observed an external review on it.
@JsonSerializable()
class TrackedIssueLinkedPR {
  final int number;
  final String url;
  final String state;
  @JsonKey(name: 'external_review_state')
  final String externalReviewState;
  @JsonKey(name: 'external_reviewer')
  final String externalReviewer;
  @JsonKey(name: 'external_review_at')
  final DateTime? externalReviewAt;

  const TrackedIssueLinkedPR({
    required this.number,
    required this.url,
    required this.state,
    required this.externalReviewState,
    required this.externalReviewer,
    this.externalReviewAt,
  });

  factory TrackedIssueLinkedPR.fromJson(Map<String, dynamic> json) =>
      _$TrackedIssueLinkedPRFromJson(json);
  Map<String, dynamic> toJson() => _$TrackedIssueLinkedPRToJson(this);
}

@JsonSerializable()
class TrackedIssue {
  final int id;
  @JsonKey(name: 'github_id')
  final int githubId;
  final String repo;
  final int number;
  final String title;
  final String body;
  final String author;
  final List<dynamic> assignees;
  final List<dynamic> labels;
  final String state;
  @JsonKey(name: 'created_at')
  final DateTime createdAt;
  @JsonKey(name: 'fetched_at')
  final DateTime fetchedAt;
  @JsonKey(defaultValue: false)
  final bool dismissed;
  @JsonKey(name: 'latest_review', includeIfNull: false)
  final TrackedIssueReview? latestReview;
  @JsonKey(name: 'linked_pr', includeIfNull: false)
  final TrackedIssueLinkedPR? linkedPR;

  const TrackedIssue({
    required this.id,
    required this.githubId,
    required this.repo,
    required this.number,
    required this.title,
    required this.body,
    required this.author,
    required this.assignees,
    required this.labels,
    required this.state,
    required this.createdAt,
    required this.fetchedAt,
    this.dismissed = false,
    this.latestReview,
    this.linkedPR,
  });

  factory TrackedIssue.fromJson(Map<String, dynamic> json) =>
      _$TrackedIssueFromJson(json);
  Map<String, dynamic> toJson() => _$TrackedIssueToJson(this);
}
