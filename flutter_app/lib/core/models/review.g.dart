// GENERATED CODE - DO NOT MODIFY BY HAND

part of 'review.dart';

// **************************************************************************
// JsonSerializableGenerator
// **************************************************************************

Review _$ReviewFromJson(Map<String, dynamic> json) => Review(
  id: (json['id'] as num).toInt(),
  prId: (json['pr_id'] as num).toInt(),
  cliUsed: json['cli_used'] as String,
  summary: json['summary'] as String,
  issues: (json['issues'] as List<dynamic>)
      .map((e) => Issue.fromJson(e as Map<String, dynamic>))
      .toList(),
  suggestions: (json['suggestions'] as List<dynamic>)
      .map((e) => e as String)
      .toList(),
  severity: json['severity'] as String,
  createdAt: DateTime.parse(json['created_at'] as String),
);

Map<String, dynamic> _$ReviewToJson(Review instance) => <String, dynamic>{
  'id': instance.id,
  'pr_id': instance.prId,
  'cli_used': instance.cliUsed,
  'summary': instance.summary,
  'issues': instance.issues,
  'suggestions': instance.suggestions,
  'severity': instance.severity,
  'created_at': instance.createdAt.toIso8601String(),
};
