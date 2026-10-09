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
  severity: json['severity'] as String,
  createdAt: DateTime.parse(json['created_at'] as String),
  inputTokens: (json['input_tokens'] as num?)?.toInt() ?? 0,
  outputTokens: (json['output_tokens'] as num?)?.toInt() ?? 0,
  cacheReadTokens: (json['cache_read_tokens'] as num?)?.toInt() ?? 0,
  costUsd: (json['cost_usd'] as num?)?.toDouble() ?? 0,
  tokensEstimated: json['tokens_estimated'] as bool? ?? false,
);

Map<String, dynamic> _$ReviewToJson(Review instance) => <String, dynamic>{
  'id': instance.id,
  'pr_id': instance.prId,
  'cli_used': instance.cliUsed,
  'summary': instance.summary,
  'issues': instance.issues,
  'severity': instance.severity,
  'created_at': instance.createdAt.toIso8601String(),
  'input_tokens': instance.inputTokens,
  'output_tokens': instance.outputTokens,
  'cache_read_tokens': instance.cacheReadTokens,
  'cost_usd': instance.costUsd,
  'tokens_estimated': instance.tokensEstimated,
};
