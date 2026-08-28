// GENERATED CODE - DO NOT MODIFY BY HAND

part of 'merge_tracking.dart';

// **************************************************************************
// JsonSerializableGenerator
// **************************************************************************

MergeCheck _$MergeCheckFromJson(Map<String, dynamic> json) => MergeCheck(
  name: json['name'] as String,
  kind: json['kind'] as String? ?? '',
  state: json['state'] as String? ?? 'pending',
  required: json['required'] as bool? ?? false,
  description: json['description'] as String? ?? '',
  app: json['app'] as String? ?? '',
  url: json['url'] as String? ?? '',
  startedAt: json['started_at'] == null
      ? null
      : DateTime.parse(json['started_at'] as String),
  completedAt: json['completed_at'] == null
      ? null
      : DateTime.parse(json['completed_at'] as String),
);

Map<String, dynamic> _$MergeCheckToJson(MergeCheck instance) =>
    <String, dynamic>{
      'name': instance.name,
      'kind': instance.kind,
      'state': instance.state,
      'required': instance.required,
      'description': instance.description,
      'app': instance.app,
      'url': instance.url,
      'started_at': ?instance.startedAt?.toIso8601String(),
      'completed_at': ?instance.completedAt?.toIso8601String(),
    };

MergeChecksSummary _$MergeChecksSummaryFromJson(Map<String, dynamic> json) =>
    MergeChecksSummary(
      total: (json['total'] as num?)?.toInt() ?? 0,
      requiredTotal: (json['required_total'] as num?)?.toInt() ?? 0,
      requiredSuccess: (json['required_success'] as num?)?.toInt() ?? 0,
      requiredPending: (json['required_pending'] as num?)?.toInt() ?? 0,
      requiredFailing: (json['required_failing'] as num?)?.toInt() ?? 0,
      optionalFailing: (json['optional_failing'] as num?)?.toInt() ?? 0,
      missingRequired:
          (json['missing_required'] as List<dynamic>?)
              ?.map((e) => e as String)
              .toList() ??
          const [],
      truncated: json['truncated'] as bool? ?? false,
    );

Map<String, dynamic> _$MergeChecksSummaryToJson(MergeChecksSummary instance) =>
    <String, dynamic>{
      'total': instance.total,
      'required_total': instance.requiredTotal,
      'required_success': instance.requiredSuccess,
      'required_pending': instance.requiredPending,
      'required_failing': instance.requiredFailing,
      'optional_failing': instance.optionalFailing,
      'missing_required': instance.missingRequired,
      'truncated': instance.truncated,
    };

MergeBlock _$MergeBlockFromJson(Map<String, dynamic> json) => MergeBlock(
  reason: json['reason'] as String,
  detail: json['detail'] as String? ?? '',
);

Map<String, dynamic> _$MergeBlockToJson(MergeBlock instance) =>
    <String, dynamic>{'reason': instance.reason, 'detail': instance.detail};

MergeDecision _$MergeDecisionFromJson(Map<String, dynamic> json) =>
    MergeDecision(
      ready: json['ready'] as bool? ?? false,
      blocks:
          (json['blocks'] as List<dynamic>?)
              ?.map((e) => MergeBlock.fromJson(e as Map<String, dynamic>))
              .toList() ??
          const [],
      checks:
          (json['checks'] as List<dynamic>?)
              ?.map((e) => MergeCheck.fromJson(e as Map<String, dynamic>))
              .toList() ??
          const [],
      checksSummary: json['checks_summary'] == null
          ? null
          : MergeChecksSummary.fromJson(
              json['checks_summary'] as Map<String, dynamic>,
            ),
    );

Map<String, dynamic> _$MergeDecisionToJson(MergeDecision instance) =>
    <String, dynamic>{
      'ready': instance.ready,
      'blocks': instance.blocks.map((e) => e.toJson()).toList(),
      'checks': instance.checks.map((e) => e.toJson()).toList(),
      'checks_summary': instance.checksSummary?.toJson(),
    };

MergeTrackingEntry _$MergeTrackingEntryFromJson(Map<String, dynamic> json) =>
    MergeTrackingEntry(
      prId: (json['pr_id'] as num).toInt(),
      repo: json['repo'] as String,
      number: (json['number'] as num).toInt(),
      title: json['title'] as String? ?? '',
      url: json['url'] as String? ?? '',
      author: json['author'] as String? ?? '',
      phase: json['phase'] as String? ?? 'idle',
      headSha: json['head_sha'] as String? ?? '',
      baseRef: json['base_ref'] as String? ?? '',
      headRef: json['head_ref'] as String? ?? '',
      blockReason: json['block_reason'] as String? ?? '',
      blockDetail: json['block_detail'] as String? ?? '',
      isAuthor: json['is_author'] as bool? ?? false,
      isAssignee: json['is_assignee'] as bool? ?? false,
      excluded: json['excluded'] as bool? ?? false,
      checksRequiredFailing:
          (json['checks_required_failing'] as num?)?.toInt() ?? 0,
      checksRequiredPending:
          (json['checks_required_pending'] as num?)?.toInt() ?? 0,
      autoMergeArmedAt: json['auto_merge_armed_at'] == null
          ? null
          : DateTime.parse(json['auto_merge_armed_at'] as String),
      autoMergeMethod: json['auto_merge_method'] as String? ?? '',
      preRebaseSha: json['pre_rebase_sha'] as String? ?? '',
      lastError: json['last_error'] as String? ?? '',
      evaluatedAt: json['evaluated_at'] == null
          ? null
          : DateTime.parse(json['evaluated_at'] as String),
      mergedAt: json['merged_at'] == null
          ? null
          : DateTime.parse(json['merged_at'] as String),
      decision: json['decision'] == null
          ? null
          : MergeDecision.fromJson(json['decision'] as Map<String, dynamic>),
    );

Map<String, dynamic> _$MergeTrackingEntryToJson(MergeTrackingEntry instance) =>
    <String, dynamic>{
      'pr_id': instance.prId,
      'repo': instance.repo,
      'number': instance.number,
      'title': instance.title,
      'url': instance.url,
      'author': instance.author,
      'phase': instance.phase,
      'head_sha': instance.headSha,
      'base_ref': instance.baseRef,
      'head_ref': instance.headRef,
      'block_reason': instance.blockReason,
      'block_detail': instance.blockDetail,
      'is_author': instance.isAuthor,
      'is_assignee': instance.isAssignee,
      'excluded': instance.excluded,
      'checks_required_failing': instance.checksRequiredFailing,
      'checks_required_pending': instance.checksRequiredPending,
      'auto_merge_armed_at': ?instance.autoMergeArmedAt?.toIso8601String(),
      'auto_merge_method': instance.autoMergeMethod,
      'pre_rebase_sha': instance.preRebaseSha,
      'last_error': instance.lastError,
      'evaluated_at': ?instance.evaluatedAt?.toIso8601String(),
      'merged_at': ?instance.mergedAt?.toIso8601String(),
      'decision': ?instance.decision?.toJson(),
    };
