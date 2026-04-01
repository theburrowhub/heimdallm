// GENERATED CODE - DO NOT MODIFY BY HAND

part of 'pr.dart';

// **************************************************************************
// JsonSerializableGenerator
// **************************************************************************

PR _$PRFromJson(Map<String, dynamic> json) => PR(
  id: (json['id'] as num).toInt(),
  githubId: (json['github_id'] as num).toInt(),
  repo: json['repo'] as String,
  number: (json['number'] as num).toInt(),
  title: json['title'] as String,
  author: json['author'] as String,
  url: json['url'] as String,
  state: json['state'] as String,
  updatedAt: DateTime.parse(json['updated_at'] as String),
  latestReview: json['latest_review'] == null
      ? null
      : Review.fromJson(json['latest_review'] as Map<String, dynamic>),
  dismissed: json['dismissed'] as bool? ?? false,
);

Map<String, dynamic> _$PRToJson(PR instance) => <String, dynamic>{
  'id': instance.id,
  'github_id': instance.githubId,
  'repo': instance.repo,
  'number': instance.number,
  'title': instance.title,
  'author': instance.author,
  'url': instance.url,
  'state': instance.state,
  'updated_at': instance.updatedAt.toIso8601String(),
  'latest_review': ?instance.latestReview,
  'dismissed': instance.dismissed,
};
