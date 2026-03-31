// GENERATED CODE - DO NOT MODIFY BY HAND

part of 'issue.dart';

// **************************************************************************
// JsonSerializableGenerator
// **************************************************************************

Issue _$IssueFromJson(Map<String, dynamic> json) => Issue(
  file: json['file'] as String,
  line: (json['line'] as num).toInt(),
  description: json['description'] as String,
  severity: json['severity'] as String,
);

Map<String, dynamic> _$IssueToJson(Issue instance) => <String, dynamic>{
  'file': instance.file,
  'line': instance.line,
  'description': instance.description,
  'severity': instance.severity,
};
