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
  final List<String> suggestions;
  final String severity;
  @JsonKey(name: 'created_at')
  final DateTime createdAt;

  const Review({
    required this.id,
    required this.prId,
    required this.cliUsed,
    required this.summary,
    required this.issues,
    required this.suggestions,
    required this.severity,
    required this.createdAt,
  });

  factory Review.fromJson(Map<String, dynamic> json) => _$ReviewFromJson(json);
  Map<String, dynamic> toJson() => _$ReviewToJson(this);
}
