import 'package:json_annotation/json_annotation.dart';
part 'issue.g.dart';

@JsonSerializable()
class Issue {
  final String file;
  final int line;
  final String description;
  final String severity;

  const Issue({
    required this.file,
    required this.line,
    required this.description,
    required this.severity,
  });

  factory Issue.fromJson(Map<String, dynamic> json) => _$IssueFromJson(json);
  Map<String, dynamic> toJson() => _$IssueToJson(this);
}
