import 'package:json_annotation/json_annotation.dart';
part 'merge_tracking.g.dart';

/// One CI check or commit status on a tracked PR's head commit.
///
/// The daemon normalises GitHub's two reporting shapes (check runs report
/// status+conclusion, commit statuses report a single state) into one
/// vocabulary, so the UI never has to know the difference.
@JsonSerializable()
class MergeCheck {
  final String name;

  /// "check_run" or "status".
  @JsonKey(defaultValue: '')
  final String kind;

  /// success | pending | failure | neutral.
  @JsonKey(defaultValue: 'pending')
  final String state;

  /// Whether this check gates the merge. An optional red check is shown but
  /// does not block, and the UI has to say so or the reader will assume it is
  /// the problem.
  @JsonKey(defaultValue: false)
  final bool required;

  @JsonKey(defaultValue: '')
  final String description;

  /// The app that ran the check ("GitHub Actions", "CircleCI"…). "build" alone
  /// is ambiguous in a repo with several CI providers.
  @JsonKey(defaultValue: '')
  final String app;

  /// Link to the run's log.
  @JsonKey(defaultValue: '')
  final String url;

  @JsonKey(name: 'started_at', includeIfNull: false)
  final DateTime? startedAt;
  @JsonKey(name: 'completed_at', includeIfNull: false)
  final DateTime? completedAt;

  const MergeCheck({
    required this.name,
    this.kind = '',
    this.state = 'pending',
    this.required = false,
    this.description = '',
    this.app = '',
    this.url = '',
    this.startedAt,
    this.completedAt,
  });

  factory MergeCheck.fromJson(Map<String, dynamic> json) =>
      _$MergeCheckFromJson(json);
  Map<String, dynamic> toJson() => _$MergeCheckToJson(this);

  bool get isFailure => state == 'failure';
  bool get isPending => state == 'pending';
  bool get isSuccess => state == 'success' || state == 'neutral';

  /// How long the check took, or null when GitHub did not report both ends.
  Duration? get duration {
    final start = startedAt;
    final end = completedAt;
    if (start == null || end == null || end.isBefore(start)) return null;
    return end.difference(start);
  }
}

/// Counts the listing renders its warning from, without walking the check list.
@JsonSerializable()
class MergeChecksSummary {
  @JsonKey(defaultValue: 0)
  final int total;
  @JsonKey(name: 'required_total', defaultValue: 0)
  final int requiredTotal;
  @JsonKey(name: 'required_success', defaultValue: 0)
  final int requiredSuccess;
  @JsonKey(name: 'required_pending', defaultValue: 0)
  final int requiredPending;
  @JsonKey(name: 'required_failing', defaultValue: 0)
  final int requiredFailing;
  @JsonKey(name: 'optional_failing', defaultValue: 0)
  final int optionalFailing;

  /// Contexts branch protection demands that have never reported. They block
  /// as hard as a failure and are easy to miss: nothing red appears anywhere.
  // No JsonKey defaultValue: the field is omitempty on the wire, so an absent
  // key falls back to the constructor default.
  @JsonKey(name: 'missing_required')
  final List<String> missingRequired;

  @JsonKey(defaultValue: false)
  final bool truncated;

  const MergeChecksSummary({
    this.total = 0,
    this.requiredTotal = 0,
    this.requiredSuccess = 0,
    this.requiredPending = 0,
    this.requiredFailing = 0,
    this.optionalFailing = 0,
    this.missingRequired = const [],
    this.truncated = false,
  });

  factory MergeChecksSummary.fromJson(Map<String, dynamic> json) =>
      _$MergeChecksSummaryFromJson(json);
  Map<String, dynamic> toJson() => _$MergeChecksSummaryToJson(this);

  /// Whether the checks warrant a warning in the listing.
  bool get anyProblem =>
      requiredFailing > 0 ||
      requiredPending > 0 ||
      missingRequired.isNotEmpty ||
      truncated;
}

/// One reason a PR is not being merged.
@JsonSerializable()
class MergeBlock {
  final String reason;
  @JsonKey(defaultValue: '')
  final String detail;

  const MergeBlock({required this.reason, this.detail = ''});

  factory MergeBlock.fromJson(Map<String, dynamic> json) =>
      _$MergeBlockFromJson(json);
  Map<String, dynamic> toJson() => _$MergeBlockToJson(this);
}

/// The explainable decision the daemon recorded for a tracked PR.
// explicitToJson so the nested blocks, checks and summary serialise as maps
// rather than as objects Dart cannot encode.
@JsonSerializable(explicitToJson: true)
class MergeDecision {
  @JsonKey(defaultValue: false)
  final bool ready;
  final List<MergeBlock> blocks;
  final List<MergeCheck> checks;
  @JsonKey(name: 'checks_summary')
  final MergeChecksSummary? checksSummary;

  const MergeDecision({
    this.ready = false,
    this.blocks = const [],
    this.checks = const [],
    this.checksSummary,
  });

  factory MergeDecision.fromJson(Map<String, dynamic> json) =>
      _$MergeDecisionFromJson(json);
  Map<String, dynamic> toJson() => _$MergeDecisionToJson(this);

  /// Checks that gate the merge, in the order the daemon sorted them
  /// (failures first, then pending).
  List<MergeCheck> get requiredChecks =>
      checks.where((c) => c.required).toList();

  /// Checks that do not gate. Shown collapsed so their noise does not hide
  /// what is actually blocking.
  List<MergeCheck> get optionalChecks =>
      checks.where((c) => !c.required).toList();
}

/// A PR the authenticated user authored or is assigned to, with its
/// merge-readiness state.
@JsonSerializable(explicitToJson: true)
class MergeTrackingEntry {
  @JsonKey(name: 'pr_id')
  final int prId;
  final String repo;
  final int number;
  @JsonKey(defaultValue: '')
  final String title;
  @JsonKey(defaultValue: '')
  final String url;
  @JsonKey(defaultValue: '')
  final String author;

  /// idle | blocked | updating | resolving | auto_merge_armed | merging |
  /// merged | abandoned.
  @JsonKey(defaultValue: 'idle')
  final String phase;

  @JsonKey(name: 'head_sha', defaultValue: '')
  final String headSha;
  @JsonKey(name: 'base_ref', defaultValue: '')
  final String baseRef;
  @JsonKey(name: 'head_ref', defaultValue: '')
  final String headRef;

  @JsonKey(name: 'block_reason', defaultValue: '')
  final String blockReason;

  /// Names the specifics — which check, which reviewer. This is the text the
  /// listing shows; a bare reason code is not actionable.
  @JsonKey(name: 'block_detail', defaultValue: '')
  final String blockDetail;

  @JsonKey(name: 'is_author', defaultValue: false)
  final bool isAuthor;
  @JsonKey(name: 'is_assignee', defaultValue: false)
  final bool isAssignee;
  @JsonKey(defaultValue: false)
  final bool excluded;

  @JsonKey(name: 'checks_required_failing', defaultValue: 0)
  final int checksRequiredFailing;
  @JsonKey(name: 'checks_required_pending', defaultValue: 0)
  final int checksRequiredPending;

  @JsonKey(name: 'auto_merge_armed_at', includeIfNull: false)
  final DateTime? autoMergeArmedAt;
  @JsonKey(name: 'auto_merge_method', defaultValue: '')
  final String autoMergeMethod;

  /// The head the branch was at before Heimdallm rewrote it. Surfaced so a bad
  /// conflict resolution is one command to undo.
  @JsonKey(name: 'pre_rebase_sha', defaultValue: '')
  final String preRebaseSha;

  @JsonKey(name: 'last_error', defaultValue: '')
  final String lastError;
  @JsonKey(name: 'evaluated_at', includeIfNull: false)
  final DateTime? evaluatedAt;
  @JsonKey(name: 'merged_at', includeIfNull: false)
  final DateTime? mergedAt;

  /// Only populated by the detail endpoint.
  @JsonKey(includeIfNull: false)
  final MergeDecision? decision;

  const MergeTrackingEntry({
    required this.prId,
    required this.repo,
    required this.number,
    this.title = '',
    this.url = '',
    this.author = '',
    this.phase = 'idle',
    this.headSha = '',
    this.baseRef = '',
    this.headRef = '',
    this.blockReason = '',
    this.blockDetail = '',
    this.isAuthor = false,
    this.isAssignee = false,
    this.excluded = false,
    this.checksRequiredFailing = 0,
    this.checksRequiredPending = 0,
    this.autoMergeArmedAt,
    this.autoMergeMethod = '',
    this.preRebaseSha = '',
    this.lastError = '',
    this.evaluatedAt,
    this.mergedAt,
    this.decision,
  });

  factory MergeTrackingEntry.fromJson(Map<String, dynamic> json) =>
      _$MergeTrackingEntryFromJson(json);
  Map<String, dynamic> toJson() => _$MergeTrackingEntryToJson(this);

  /// Whether the primary block reason is a CI problem.
  bool get blockReasonIsChecks => const {
    'checks_failing',
    'checks_pending',
    'required_check_missing',
    'checks_unknown',
  }.contains(blockReason);

  /// Whether the prominent CI warning should be shown.
  ///
  /// A failing required check earns the warning even when it is not the
  /// *primary* blocker. A PR that is both behind its base and failing a check
  /// reports `behind_base` — correct, because that is the next action — but the
  /// failing check is the part a human has to fix, and burying it under a
  /// milder message is exactly the outcome this feature exists to prevent.
  bool get blockedByChecks =>
      !isTerminal && (blockReasonIsChecks || checksRequiredFailing > 0);

  bool get hasFailingChecks => checksRequiredFailing > 0;
  bool get hasPendingChecks => checksRequiredPending > 0;
  bool get isMerged => phase == 'merged';
  bool get isTerminal => phase == 'merged' || phase == 'abandoned';
  bool get autoMergeArmed => phase == 'auto_merge_armed';
}
