import 'package:json_annotation/json_annotation.dart';
import 'review.dart';
part 'pr.g.dart';

@JsonSerializable()
class PR {
  final int id;
  @JsonKey(name: 'github_id')
  final int githubId;
  final String repo;
  final int number;
  final String title;
  final String author;
  final String url;
  final String state;
  @JsonKey(name: 'updated_at')
  final DateTime updatedAt;
  @JsonKey(name: 'latest_review', includeIfNull: false)
  final Review? latestReview;
  @JsonKey(name: 'dismissed', defaultValue: false)
  final bool dismissed;

  const PR({
    required this.id,
    required this.githubId,
    required this.repo,
    required this.number,
    required this.title,
    required this.author,
    required this.url,
    required this.state,
    required this.updatedAt,
    this.latestReview,
    this.dismissed = false,
  });

  factory PR.fromJson(Map<String, dynamic> json) => _$PRFromJson(json);
  Map<String, dynamic> toJson() => _$PRToJson(this);
}
