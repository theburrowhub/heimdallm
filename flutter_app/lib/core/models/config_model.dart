import '../instances/models.dart' show ClusterRole;

/// Severities `never_approve_min_severity` accepts, in ascending order.
/// Mirrors the daemon's validator (config.validateNeverApproveMinSeverity).
const neverApproveMinSeverityOptions = ['low', 'medium', 'high'];

/// Threshold the daemon applies when `never_approve_min_severity` is unset at
/// every scope. Mirrors pipeline.DefaultNeverApproveMinSeverity — keep both in
/// sync, otherwise the dropdown shows a value the daemon is not using.
const defaultNeverApproveMinSeverity = 'medium';

/// Per-agent CLI execution settings.
/// Stored under `ai.agents.<name>` in config.toml.
class CLIAgentConfig {
  final String model; // --model value ('' = use CLI default)
  final int maxTurns; // claude: --max-turns (0 = not set)
  final String approvalMode; // codex: --ask-for-approval ('' = not set)
  final String extraFlags; // free-form additional CLI flags (space-separated)
  final String?
  promptId; // agent-level prompt override (null = use global default)

  // Claude-specific flags
  final String effort; // '' | 'low' | 'medium' | 'high' | 'max'
  final String
  permissionMode; // '' | 'default' | 'auto' | 'bypassPermissions' | 'acceptEdits' | 'dontAsk'
  final bool bare; // --bare
  final bool dangerouslySkipPerms; // --dangerously-skip-permissions
  final bool noSessionPersistence; // --no-session-persistence

  /// Reviews this agent may run per window; when it is spent the daemon falls
  /// back to the next agent or defers the review.
  final ReviewLimits reviewLimits;

  const CLIAgentConfig({
    this.model = '',
    this.maxTurns = 0,
    this.approvalMode = '',
    this.extraFlags = '',
    this.promptId,
    this.effort = '',
    this.permissionMode = '',
    this.bare = false,
    this.dangerouslySkipPerms = false,
    this.noSessionPersistence = false,
    this.reviewLimits = const ReviewLimits(),
  });

  bool get hasConfig =>
      model.isNotEmpty ||
      maxTurns > 0 ||
      approvalMode.isNotEmpty ||
      extraFlags.isNotEmpty ||
      promptId != null ||
      effort.isNotEmpty ||
      permissionMode.isNotEmpty ||
      bare ||
      dangerouslySkipPerms ||
      noSessionPersistence ||
      !reviewLimits.isEmpty;

  CLIAgentConfig copyWith({
    String? model,
    int? maxTurns,
    String? approvalMode,
    String? extraFlags,
    Object? promptId = _sentinel,
    String? effort,
    String? permissionMode,
    bool? bare,
    bool? dangerouslySkipPerms,
    bool? noSessionPersistence,
    ReviewLimits? reviewLimits,
  }) => CLIAgentConfig(
    model: model ?? this.model,
    maxTurns: maxTurns ?? this.maxTurns,
    approvalMode: approvalMode ?? this.approvalMode,
    extraFlags: extraFlags ?? this.extraFlags,
    promptId: promptId == _sentinel ? this.promptId : promptId as String?,
    effort: effort ?? this.effort,
    permissionMode: permissionMode ?? this.permissionMode,
    bare: bare ?? this.bare,
    dangerouslySkipPerms: dangerouslySkipPerms ?? this.dangerouslySkipPerms,
    noSessionPersistence: noSessionPersistence ?? this.noSessionPersistence,
    reviewLimits: reviewLimits ?? this.reviewLimits,
  );

  factory CLIAgentConfig.fromJson(Map<String, dynamic> json) => CLIAgentConfig(
    model: (json['model'] as String?) ?? '',
    maxTurns: (json['max_turns'] as int?) ?? 0,
    approvalMode: (json['approval_mode'] as String?) ?? '',
    extraFlags: (json['extra_flags'] as String?) ?? '',
    promptId: _nonEmpty(json['prompt']),
    effort: (json['effort'] as String?) ?? '',
    permissionMode: (json['permission_mode'] as String?) ?? '',
    bare: (json['bare'] as bool?) ?? false,
    dangerouslySkipPerms: (json['dangerously_skip_perms'] as bool?) ?? false,
    noSessionPersistence: (json['no_session_persistence'] as bool?) ?? false,
    reviewLimits:
        ReviewLimits.maybeFromJson(json['review_limits']) ??
        const ReviewLimits(),
  );

  // Temporary fallback until safe provider capability discovery lands (#734).
  static const modelOptions = <String, List<String>>{
    'claude': [
      'claude-fable-5',
      'claude-opus-5',
      'claude-sonnet-5',
      'claude-haiku-4-5-20251001',
    ],
    'gemini': [
      // Newer bare Flash IDs are omitted while Gemini CLI substitutes 3.5.
      'gemini-3.1-pro-preview',
      'gemini-3.5-flash',
      'gemini-3.5-flash-lite',
      'gemini-3.1-flash-lite',
      'gemini-2.5-pro',
      'gemini-2.5-flash',
      'gemini-2.5-flash-lite',
    ],
    'codex': [
      'gpt-5.6-sol',
      'gpt-5.6-terra',
      'gpt-5.6-luna',
      'gpt-5.5',
      'gpt-5.3-codex-spark',
    ],
  };

  static const approvalModeOptions = [
    'never',
    'on-request',
    'on-failure',
    'untrusted',
    'full-auto',
    'auto-edit',
    'suggest',
  ];
  static const effortOptions = ['low', 'medium', 'high', 'max'];
  static const permissionModeOptions = [
    'default',
    'auto',
    'bypassPermissions',
    'acceptEdits',
    'dontAsk',
  ];
}

/// Per-organization / per-repository merge-tracking override.
///
/// Every null field means "inherit". Empty strings are treated the same way
/// when parsing or serializing because the daemon uses an empty TOML string as
/// the unset value for string overrides.
class MergeTrackingOverride {
  final bool? enabled;
  final bool? enableAutoMerge;
  final bool? updateBranch;
  final bool? resolveConflicts;
  final bool? merge;
  final String? mergeMethod;
  final bool? includeAssigned;
  final bool? requireApproval;
  final int? maxUpdateAttempts;
  final int? maxResolveAttempts;
  final int? maxMergeAttempts;
  final String? actionCooldown;
  final String? resolveTimeout;
  final String? resolveEffort;

  const MergeTrackingOverride({
    this.enabled,
    this.enableAutoMerge,
    this.updateBranch,
    this.resolveConflicts,
    this.merge,
    this.mergeMethod,
    this.includeAssigned,
    this.requireApproval,
    this.maxUpdateAttempts,
    this.maxResolveAttempts,
    this.maxMergeAttempts,
    this.actionCooldown,
    this.resolveTimeout,
    this.resolveEffort,
  });

  bool get isEmpty =>
      enabled == null &&
      enableAutoMerge == null &&
      updateBranch == null &&
      resolveConflicts == null &&
      merge == null &&
      _nonEmpty(mergeMethod) == null &&
      includeAssigned == null &&
      requireApproval == null &&
      maxUpdateAttempts == null &&
      maxResolveAttempts == null &&
      maxMergeAttempts == null &&
      _nonEmpty(actionCooldown) == null &&
      _nonEmpty(resolveTimeout) == null &&
      _nonEmpty(resolveEffort) == null;

  MergeTrackingOverride copyWith({
    Object? enabled = _sentinel,
    Object? enableAutoMerge = _sentinel,
    Object? updateBranch = _sentinel,
    Object? resolveConflicts = _sentinel,
    Object? merge = _sentinel,
    Object? mergeMethod = _sentinel,
    Object? includeAssigned = _sentinel,
    Object? requireApproval = _sentinel,
    Object? maxUpdateAttempts = _sentinel,
    Object? maxResolveAttempts = _sentinel,
    Object? maxMergeAttempts = _sentinel,
    Object? actionCooldown = _sentinel,
    Object? resolveTimeout = _sentinel,
    Object? resolveEffort = _sentinel,
  }) => MergeTrackingOverride(
    enabled: enabled == _sentinel ? this.enabled : enabled as bool?,
    enableAutoMerge: enableAutoMerge == _sentinel
        ? this.enableAutoMerge
        : enableAutoMerge as bool?,
    updateBranch: updateBranch == _sentinel
        ? this.updateBranch
        : updateBranch as bool?,
    resolveConflicts: resolveConflicts == _sentinel
        ? this.resolveConflicts
        : resolveConflicts as bool?,
    merge: merge == _sentinel ? this.merge : merge as bool?,
    mergeMethod: mergeMethod == _sentinel
        ? this.mergeMethod
        : mergeMethod as String?,
    includeAssigned: includeAssigned == _sentinel
        ? this.includeAssigned
        : includeAssigned as bool?,
    requireApproval: requireApproval == _sentinel
        ? this.requireApproval
        : requireApproval as bool?,
    maxUpdateAttempts: maxUpdateAttempts == _sentinel
        ? this.maxUpdateAttempts
        : maxUpdateAttempts as int?,
    maxResolveAttempts: maxResolveAttempts == _sentinel
        ? this.maxResolveAttempts
        : maxResolveAttempts as int?,
    maxMergeAttempts: maxMergeAttempts == _sentinel
        ? this.maxMergeAttempts
        : maxMergeAttempts as int?,
    actionCooldown: actionCooldown == _sentinel
        ? this.actionCooldown
        : actionCooldown as String?,
    resolveTimeout: resolveTimeout == _sentinel
        ? this.resolveTimeout
        : resolveTimeout as String?,
    resolveEffort: resolveEffort == _sentinel
        ? this.resolveEffort
        : resolveEffort as String?,
  );

  factory MergeTrackingOverride.fromJson(Map<String, dynamic> json) =>
      MergeTrackingOverride(
        enabled: json['enabled'] as bool?,
        enableAutoMerge: json['enable_auto_merge'] as bool?,
        updateBranch: json['update_branch'] as bool?,
        resolveConflicts: json['resolve_conflicts'] as bool?,
        merge: json['merge'] as bool?,
        mergeMethod: _nonEmpty(json['merge_method']),
        includeAssigned: json['include_assigned'] as bool?,
        requireApproval: json['require_approval'] as bool?,
        maxUpdateAttempts: (json['max_update_attempts'] as num?)?.toInt(),
        maxResolveAttempts: (json['max_resolve_attempts'] as num?)?.toInt(),
        maxMergeAttempts: (json['max_merge_attempts'] as num?)?.toInt(),
        actionCooldown: _nonEmpty(json['action_cooldown']),
        resolveTimeout: _nonEmpty(json['resolve_timeout']),
        resolveEffort: _nonEmpty(json['resolve_effort']),
      );

  /// JSON accepted by the scoped PATCH endpoints. Unset values are omitted;
  /// use [diffMergeTrackingOverrides] when nulls must be retained as deletes.
  Map<String, dynamic> toJson() => {
    if (enabled != null) 'enabled': enabled,
    if (enableAutoMerge != null) 'enable_auto_merge': enableAutoMerge,
    if (updateBranch != null) 'update_branch': updateBranch,
    if (resolveConflicts != null) 'resolve_conflicts': resolveConflicts,
    if (merge != null) 'merge': merge,
    'merge_method': ?_nonEmpty(mergeMethod),
    if (includeAssigned != null) 'include_assigned': includeAssigned,
    if (requireApproval != null) 'require_approval': requireApproval,
    if (maxUpdateAttempts != null) 'max_update_attempts': maxUpdateAttempts,
    if (maxResolveAttempts != null) 'max_resolve_attempts': maxResolveAttempts,
    if (maxMergeAttempts != null) 'max_merge_attempts': maxMergeAttempts,
    'action_cooldown': ?_nonEmpty(actionCooldown),
    'resolve_timeout': ?_nonEmpty(resolveTimeout),
    'resolve_effort': ?_nonEmpty(resolveEffort),
  };
}

/// Produces the scoped PATCH payload, retaining null for fields that changed
/// back to inherited so [ApiClient] can translate them into DELETE requests.
Map<String, dynamic> diffMergeTrackingOverrides(
  MergeTrackingOverride before,
  MergeTrackingOverride after,
) {
  final diff = <String, dynamic>{};

  void add(String key, Object? oldValue, Object? newValue) {
    if (oldValue != newValue) diff[key] = newValue;
  }

  add('enabled', before.enabled, after.enabled);
  add('enable_auto_merge', before.enableAutoMerge, after.enableAutoMerge);
  add('update_branch', before.updateBranch, after.updateBranch);
  add('resolve_conflicts', before.resolveConflicts, after.resolveConflicts);
  add('merge', before.merge, after.merge);
  add(
    'merge_method',
    _nonEmpty(before.mergeMethod),
    _nonEmpty(after.mergeMethod),
  );
  add('include_assigned', before.includeAssigned, after.includeAssigned);
  add('require_approval', before.requireApproval, after.requireApproval);
  add('max_update_attempts', before.maxUpdateAttempts, after.maxUpdateAttempts);
  add(
    'max_resolve_attempts',
    before.maxResolveAttempts,
    after.maxResolveAttempts,
  );
  add('max_merge_attempts', before.maxMergeAttempts, after.maxMergeAttempts);
  add(
    'action_cooldown',
    _nonEmpty(before.actionCooldown),
    _nonEmpty(after.actionCooldown),
  );
  add(
    'resolve_timeout',
    _nonEmpty(before.resolveTimeout),
    _nonEmpty(after.resolveTimeout),
  );
  add(
    'resolve_effort',
    _nonEmpty(before.resolveEffort),
    _nonEmpty(after.resolveEffort),
  );
  return diff;
}

/// Per-repo AI override. null fields mean "use global default".
class RepoConfig {
  // Per-feature activation (null = inherit global behavior)
  final bool? prEnabled; // PR auto-review
  final MergeTrackingOverride _mergeTracking;
  final bool? _legacyMtEnabled;

  /// Complete scoped override. The legacy constructor argument is folded in
  /// lazily so existing const call sites can keep using `mtEnabled:`.
  MergeTrackingOverride get mergeTracking {
    if (_mergeTracking.enabled != null || _legacyMtEnabled == null) {
      return _mergeTracking;
    }
    return _mergeTracking.copyWith(enabled: _legacyMtEnabled);
  }

  /// Compatibility alias used by the existing repo list and detail switch.
  bool? get mtEnabled => _mergeTracking.enabled ?? _legacyMtEnabled;

  // General
  final String? localDir; // local repo directory for full-repo analysis
  final String? cloneDir;
  final DateTime?
  firstSeenAt; // when the daemon first discovered this repo (null = unknown)

  // PR Review config
  final String? aiPrimary; // null = use global
  final String? aiFallback; // null = use global
  final String? promptId; // null = use globally active prompt
  final String? reviewMode; // null = use global ("single" | "multi")
  final bool? neverApproveWithIssues;
  final String? neverApproveMinSeverity;

  /// This repo's own review budget (null = none), counted over this repo's
  /// reviews only. Removing it is a DELETE, never a diffed null.
  final ReviewLimits? reviewLimits;

  /// True when this repo's org has a bare entry in `non_monitored`
  /// (theburrowhub/heimdallm#828). Derived by `AppConfig.fromJson` from
  /// `nonMonitoredOrgs`, never set directly by the UI — it is not part of
  /// `isMonitored`/`hasAiOverride` so the TOML-write path (which computes
  /// `github.repositories` from per-repo settings alone) is unaffected; use
  /// [isEffectivelyMonitored] for display.
  final bool excludedByOrg;

  const RepoConfig({
    this.prEnabled,
    bool? mtEnabled,
    MergeTrackingOverride mergeTracking = const MergeTrackingOverride(),
    this.localDir,
    this.cloneDir,
    this.aiPrimary,
    this.aiFallback,
    this.promptId,
    this.reviewMode,
    this.neverApproveWithIssues,
    this.neverApproveMinSeverity,
    this.reviewLimits,
    this.firstSeenAt,
    this.excludedByOrg = false,
  }) : _legacyMtEnabled = mtEnabled,
       _mergeTracking = mergeTracking;

  /// True if any feature is actively enabled (per-repo or inherited).
  /// Used by the TOML writer to decide which repos go in `repositories`.
  /// Deliberately blind to [excludedByOrg] — see its doc comment. Use
  /// [isEffectivelyMonitored] to classify monitored vs not-monitored for
  /// display.
  bool get isMonitored {
    // Merge tracking discovery intersects with github.repositories just like
    // PR review. A repo-level merge-tracking opt-in therefore keeps the repo
    // monitored even when PR review is off.
    return (prEnabled ?? false) || mtEnabled == true;
  }

  /// What the daemon actually does with this repo right now: [isMonitored]
  /// unless the whole org is excluded, in which case MergeRepos filters the
  /// repo out regardless of its own settings (theburrowhub/heimdallm#828).
  bool get isEffectivelyMonitored => isMonitored && !excludedByOrg;

  /// Legacy getter — repos with any override need to be written to TOML.
  bool get hasAiOverride =>
      prEnabled != null ||
      !mergeTracking.isEmpty ||
      aiPrimary != null ||
      aiFallback != null ||
      promptId != null ||
      reviewMode != null ||
      (localDir != null && localDir!.isNotEmpty) ||
      cloneDir != null ||
      neverApproveWithIssues != null ||
      neverApproveMinSeverity != null;

  /// LED status for each feature: 'off', 'global', 'repo'
  String prLedStatus(bool globalMonitored) {
    if (prEnabled == true) return 'repo';
    if (prEnabled == false) return 'off';
    return globalMonitored ? 'global' : 'off';
  }

  RepoConfig copyWith({
    Object? prEnabled = _sentinel,
    Object? mtEnabled = _sentinel,
    Object? mergeTracking = _sentinel,
    Object? localDir = _sentinel,
    Object? cloneDir = _sentinel,
    Object? aiPrimary = _sentinel,
    Object? aiFallback = _sentinel,
    Object? promptId = _sentinel,
    Object? reviewMode = _sentinel,
    Object? neverApproveWithIssues = _sentinel,
    Object? neverApproveMinSeverity = _sentinel,
    Object? reviewLimits = _sentinel,
    Object? firstSeenAt = _sentinel,
    bool? excludedByOrg,
  }) {
    final requestedMergeTracking = mergeTracking == _sentinel
        ? this.mergeTracking
        : (mergeTracking as MergeTrackingOverride?) ??
              const MergeTrackingOverride();
    final updatedMergeTracking = mtEnabled == _sentinel
        ? requestedMergeTracking
        : requestedMergeTracking.copyWith(enabled: mtEnabled as bool?);
    return RepoConfig(
      prEnabled: prEnabled == _sentinel ? this.prEnabled : prEnabled as bool?,
      mergeTracking: updatedMergeTracking,
      localDir: localDir == _sentinel ? this.localDir : localDir as String?,
      cloneDir: cloneDir == _sentinel ? this.cloneDir : cloneDir as String?,
      aiPrimary: aiPrimary == _sentinel ? this.aiPrimary : aiPrimary as String?,
      aiFallback: aiFallback == _sentinel
          ? this.aiFallback
          : aiFallback as String?,
      promptId: promptId == _sentinel ? this.promptId : promptId as String?,
      reviewMode: reviewMode == _sentinel
          ? this.reviewMode
          : reviewMode as String?,
      neverApproveWithIssues: neverApproveWithIssues == _sentinel
          ? this.neverApproveWithIssues
          : neverApproveWithIssues as bool?,
      neverApproveMinSeverity: neverApproveMinSeverity == _sentinel
          ? this.neverApproveMinSeverity
          : neverApproveMinSeverity as String?,
      reviewLimits: reviewLimits == _sentinel
          ? this.reviewLimits
          : reviewLimits as ReviewLimits?,
      firstSeenAt: firstSeenAt == _sentinel
          ? this.firstSeenAt
          : firstSeenAt as DateTime?,
      // Derived, not user-editable — carried forward unless this specific
      // call is the one marking it (see doc comment).
      excludedByOrg: excludedByOrg ?? this.excludedByOrg,
    );
  }
}

/// Per-organization override. null fields mean "inherit global".
class OrgConfig {
  final String? aiPrimary;
  final String? aiFallback;
  final String? promptId;
  final String? reviewMode;
  final String? localDir;
  final String? cloneDir;
  final MergeTrackingOverride _mergeTracking;
  final bool? _legacyMtEnabled;

  /// Complete scoped override. See the equivalent getter on [RepoConfig].
  MergeTrackingOverride get mergeTracking {
    if (_mergeTracking.enabled != null || _legacyMtEnabled == null) {
      return _mergeTracking;
    }
    return _mergeTracking.copyWith(enabled: _legacyMtEnabled);
  }

  /// Compatibility alias used by existing organization/repository widgets.
  bool? get mtEnabled => _mergeTracking.enabled ?? _legacyMtEnabled;
  final bool? neverApproveWithIssues;
  final String? neverApproveMinSeverity;

  /// Review budget for every repo in this org (null = none).
  final ReviewLimits? reviewLimits;

  const OrgConfig({
    this.aiPrimary,
    this.aiFallback,
    this.promptId,
    this.reviewMode,
    this.localDir,
    this.cloneDir,
    bool? mtEnabled,
    MergeTrackingOverride mergeTracking = const MergeTrackingOverride(),
    this.neverApproveWithIssues,
    this.neverApproveMinSeverity,
    this.reviewLimits,
  }) : _legacyMtEnabled = mtEnabled,
       _mergeTracking = mergeTracking;

  bool get hasOverride =>
      aiPrimary != null ||
      aiFallback != null ||
      promptId != null ||
      reviewMode != null ||
      localDir != null ||
      cloneDir != null ||
      neverApproveWithIssues != null ||
      neverApproveMinSeverity != null ||
      reviewLimits != null ||
      !mergeTracking.isEmpty;

  OrgConfig copyWith({
    Object? aiPrimary = _sentinel,
    Object? aiFallback = _sentinel,
    Object? promptId = _sentinel,
    Object? reviewMode = _sentinel,
    Object? localDir = _sentinel,
    Object? cloneDir = _sentinel,
    Object? mtEnabled = _sentinel,
    Object? mergeTracking = _sentinel,
    Object? neverApproveWithIssues = _sentinel,
    Object? neverApproveMinSeverity = _sentinel,
    Object? reviewLimits = _sentinel,
  }) {
    final requestedMergeTracking = mergeTracking == _sentinel
        ? this.mergeTracking
        : (mergeTracking as MergeTrackingOverride?) ??
              const MergeTrackingOverride();
    final updatedMergeTracking = mtEnabled == _sentinel
        ? requestedMergeTracking
        : requestedMergeTracking.copyWith(enabled: mtEnabled as bool?);
    return OrgConfig(
      aiPrimary: aiPrimary == _sentinel ? this.aiPrimary : aiPrimary as String?,
      aiFallback: aiFallback == _sentinel
          ? this.aiFallback
          : aiFallback as String?,
      promptId: promptId == _sentinel ? this.promptId : promptId as String?,
      reviewMode: reviewMode == _sentinel
          ? this.reviewMode
          : reviewMode as String?,
      localDir: localDir == _sentinel ? this.localDir : localDir as String?,
      cloneDir: cloneDir == _sentinel ? this.cloneDir : cloneDir as String?,
      mergeTracking: updatedMergeTracking,
      neverApproveWithIssues: neverApproveWithIssues == _sentinel
          ? this.neverApproveWithIssues
          : neverApproveWithIssues as bool?,
      neverApproveMinSeverity: neverApproveMinSeverity == _sentinel
          ? this.neverApproveMinSeverity
          : neverApproveMinSeverity as String?,
      reviewLimits: reviewLimits == _sentinel
          ? this.reviewLimits
          : reviewLimits as ReviewLimits?,
    );
  }

  factory OrgConfig.fromJson(Map<String, dynamic> json) {
    return OrgConfig(
      aiPrimary: _nonEmpty(json['primary']),
      aiFallback: _nonEmpty(json['fallback']),
      promptId: _nonEmpty(json['prompt']),
      reviewMode: _nonEmpty(json['review_mode']),
      localDir: _nonEmpty(json['local_dir']),
      cloneDir: _nonEmpty(json['clone_dir']),
      neverApproveWithIssues: json['never_approve_with_issues'] as bool?,
      neverApproveMinSeverity: _nonEmpty(json['never_approve_min_severity']),
      reviewLimits: ReviewLimits.maybeFromJson(json['review_limits']),
    );
  }
}

const _sentinel = Object();

/// Returns null for empty or null strings — prevents DropdownButtonFormField
/// assertion errors when Go zero-value strings ("") arrive from the daemon.
String? _nonEmpty(dynamic v) {
  final s = v as String?;
  return (s == null || s.isEmpty) ? null : s;
}

class MergeTrackingConfig {
  final bool enabled;
  final bool enableAutoMerge;
  final bool updateBranch;
  final bool resolveConflicts;
  final bool merge;
  final String mergeMethod; // 'squash' | 'merge' | 'rebase'
  final bool includeAssigned;
  final bool requireApproval;
  final String pollInterval; // empty = inherit the shared poll interval
  final int maxPrsPerTick;
  final int maxUpdateAttempts;
  final int maxResolveAttempts;
  final int maxMergeAttempts;
  final String actionCooldown;
  final String resolveTimeout;
  final String resolveEffort; // 'low' | 'medium' | 'high' | 'max'
  final Map<String, MergeTrackingOverride> orgs;
  final Map<String, MergeTrackingOverride> repos;

  const MergeTrackingConfig({
    this.enabled = false,
    this.enableAutoMerge = false,
    this.updateBranch = false,
    this.resolveConflicts = false,
    this.merge = false,
    this.mergeMethod = 'squash',
    this.includeAssigned = false,
    this.requireApproval = false,
    this.pollInterval = '',
    this.maxPrsPerTick = 20,
    this.maxUpdateAttempts = 3,
    this.maxResolveAttempts = 2,
    this.maxMergeAttempts = 3,
    this.actionCooldown = '10m',
    this.resolveTimeout = '30m',
    this.resolveEffort = 'high',
    this.orgs = const {},
    this.repos = const {},
  });

  MergeTrackingConfig copyWith({
    bool? enabled,
    bool? enableAutoMerge,
    bool? updateBranch,
    bool? resolveConflicts,
    bool? merge,
    String? mergeMethod,
    bool? includeAssigned,
    bool? requireApproval,
    String? pollInterval,
    int? maxPrsPerTick,
    int? maxUpdateAttempts,
    int? maxResolveAttempts,
    int? maxMergeAttempts,
    String? actionCooldown,
    String? resolveTimeout,
    String? resolveEffort,
    Map<String, MergeTrackingOverride>? orgs,
    Map<String, MergeTrackingOverride>? repos,
  }) => MergeTrackingConfig(
    enabled: enabled ?? this.enabled,
    enableAutoMerge: enableAutoMerge ?? this.enableAutoMerge,
    updateBranch: updateBranch ?? this.updateBranch,
    resolveConflicts: resolveConflicts ?? this.resolveConflicts,
    merge: merge ?? this.merge,
    mergeMethod: mergeMethod ?? this.mergeMethod,
    includeAssigned: includeAssigned ?? this.includeAssigned,
    requireApproval: requireApproval ?? this.requireApproval,
    pollInterval: pollInterval ?? this.pollInterval,
    maxPrsPerTick: maxPrsPerTick ?? this.maxPrsPerTick,
    maxUpdateAttempts: maxUpdateAttempts ?? this.maxUpdateAttempts,
    maxResolveAttempts: maxResolveAttempts ?? this.maxResolveAttempts,
    maxMergeAttempts: maxMergeAttempts ?? this.maxMergeAttempts,
    actionCooldown: actionCooldown ?? this.actionCooldown,
    resolveTimeout: resolveTimeout ?? this.resolveTimeout,
    resolveEffort: resolveEffort ?? this.resolveEffort,
    orgs: orgs ?? this.orgs,
    repos: repos ?? this.repos,
  );

  /// Resolves one scoped override on top of these global values. Poll cadence
  /// and per-tick limits intentionally stay global-only, matching the daemon.
  MergeTrackingConfig applyOverride(MergeTrackingOverride o) => copyWith(
    enabled: o.enabled,
    enableAutoMerge: o.enableAutoMerge,
    updateBranch: o.updateBranch,
    resolveConflicts: o.resolveConflicts,
    merge: o.merge,
    mergeMethod: _nonEmpty(o.mergeMethod),
    includeAssigned: o.includeAssigned,
    requireApproval: o.requireApproval,
    maxUpdateAttempts: o.maxUpdateAttempts,
    maxResolveAttempts: o.maxResolveAttempts,
    maxMergeAttempts: o.maxMergeAttempts,
    actionCooldown: _nonEmpty(o.actionCooldown),
    resolveTimeout: _nonEmpty(o.resolveTimeout),
    resolveEffort: _nonEmpty(o.resolveEffort),
  );

  factory MergeTrackingConfig.fromJson(Map<String, dynamic> json) {
    Map<String, MergeTrackingOverride> parseOverrides(String key) {
      final raw = json[key] as Map<String, dynamic>?;
      if (raw == null || raw.isEmpty) return const {};
      return {
        for (final entry in raw.entries)
          if (entry.value is Map<String, dynamic>)
            entry.key: MergeTrackingOverride.fromJson(
              entry.value as Map<String, dynamic>,
            ),
      };
    }

    return MergeTrackingConfig(
      enabled: json['enabled'] as bool? ?? false,
      enableAutoMerge: json['enable_auto_merge'] as bool? ?? false,
      updateBranch: json['update_branch'] as bool? ?? false,
      resolveConflicts: json['resolve_conflicts'] as bool? ?? false,
      merge: json['merge'] as bool? ?? false,
      mergeMethod: json['merge_method'] as String? ?? 'squash',
      includeAssigned: json['include_assigned'] as bool? ?? false,
      requireApproval: json['require_approval'] as bool? ?? false,
      pollInterval: json['poll_interval'] as String? ?? '',
      maxPrsPerTick: (json['max_prs_per_tick'] as num?)?.toInt() ?? 20,
      maxUpdateAttempts: (json['max_update_attempts'] as num?)?.toInt() ?? 3,
      maxResolveAttempts: (json['max_resolve_attempts'] as num?)?.toInt() ?? 2,
      maxMergeAttempts: (json['max_merge_attempts'] as num?)?.toInt() ?? 3,
      actionCooldown: json['action_cooldown'] as String? ?? '10m',
      resolveTimeout: json['resolve_timeout'] as String? ?? '30m',
      resolveEffort: json['resolve_effort'] as String? ?? 'high',
      orgs: parseOverrides('orgs'),
      repos: parseOverrides('repos'),
    );
  }

  Map<String, dynamic> toJson() => {
    'enabled': enabled,
    'enable_auto_merge': enableAutoMerge,
    'update_branch': updateBranch,
    'resolve_conflicts': resolveConflicts,
    'merge': merge,
    'merge_method': mergeMethod,
    'include_assigned': includeAssigned,
    'require_approval': requireApproval,
    'poll_interval': pollInterval,
    'max_prs_per_tick': maxPrsPerTick,
    'max_update_attempts': maxUpdateAttempts,
    'max_resolve_attempts': maxResolveAttempts,
    'max_merge_attempts': maxMergeAttempts,
    'action_cooldown': actionCooldown,
    'resolve_timeout': resolveTimeout,
    'resolve_effort': resolveEffort,
    'orgs': {for (final entry in orgs.entries) entry.key: entry.value.toJson()},
    'repos': {
      for (final entry in repos.entries) entry.key: entry.value.toJson(),
    },
  };
}

/// `[my_prs]`: the watch over the operator's own open PRs in the monitored
/// repositories — who each one is waiting on, which went quiet, and how the
/// app nudges about them. Observation only; it never writes to GitHub.
class MyPrsConfig {
  final bool enabled;
  final bool includeAssigned;

  /// Inactivity threshold ("90m", "12h", "3d"); "0" disables stale detection.
  final String staleAfter;
  final bool notifyTransitions;
  final bool digestEnabled;

  /// Local time of day of the daily digest, "HH:MM".
  final String digestTime;

  const MyPrsConfig({
    this.enabled = true,
    this.includeAssigned = true,
    this.staleAfter = '3d',
    this.notifyTransitions = false,
    this.digestEnabled = true,
    this.digestTime = '10:00',
  });

  MyPrsConfig copyWith({
    bool? enabled,
    bool? includeAssigned,
    String? staleAfter,
    bool? notifyTransitions,
    bool? digestEnabled,
    String? digestTime,
  }) => MyPrsConfig(
    enabled: enabled ?? this.enabled,
    includeAssigned: includeAssigned ?? this.includeAssigned,
    staleAfter: staleAfter ?? this.staleAfter,
    notifyTransitions: notifyTransitions ?? this.notifyTransitions,
    digestEnabled: digestEnabled ?? this.digestEnabled,
    digestTime: digestTime ?? this.digestTime,
  );

  factory MyPrsConfig.fromJson(Map<String, dynamic> json) => MyPrsConfig(
    enabled: json['enabled'] as bool? ?? true,
    includeAssigned: json['include_assigned'] as bool? ?? true,
    staleAfter: _nonEmpty(json['stale_after']) ?? '3d',
    notifyTransitions: json['notify_transitions'] as bool? ?? false,
    digestEnabled: json['digest_enabled'] as bool? ?? true,
    digestTime: _nonEmpty(json['digest_time']) ?? '10:00',
  );

  Map<String, dynamic> toJson() => {
    'enabled': enabled,
    'include_assigned': includeAssigned,
    'stale_after': staleAfter,
    'notify_transitions': notifyTransitions,
    'digest_enabled': digestEnabled,
    'digest_time': digestTime,
  };

  /// Parses [staleAfter] the way the daemon does (Go durations plus a `d`
  /// suffix). Null when detection is off or the value is not understood.
  Duration? get staleAfterDuration => parseHumanDuration(staleAfter);

  /// The digest's hour and minute, or null when [digestTime] is malformed.
  ({int hour, int minute})? get digestHourMinute {
    final m = RegExp(r'^(\d{1,2}):(\d{2})$').firstMatch(digestTime.trim());
    if (m == null) return null;
    final h = int.parse(m.group(1)!);
    final min = int.parse(m.group(2)!);
    if (h > 23 || min > 59) return null;
    return (hour: h, minute: min);
  }

  @override
  bool operator ==(Object other) =>
      other is MyPrsConfig &&
      other.enabled == enabled &&
      other.includeAssigned == includeAssigned &&
      other.staleAfter == staleAfter &&
      other.notifyTransitions == notifyTransitions &&
      other.digestEnabled == digestEnabled &&
      other.digestTime == digestTime;

  @override
  int get hashCode => Object.hash(
    enabled,
    includeAssigned,
    staleAfter,
    notifyTransitions,
    digestEnabled,
    digestTime,
  );
}

/// Parses a Go-style duration ("1h30m", "90m", "45s") or a number of days
/// ("3d", "1.5d"), mirroring the daemon's `config.ParseHumanDuration`.
/// Returns null for "0", an empty string or anything unparseable.
Duration? parseHumanDuration(String raw) {
  // The daemon rejects anything past Go's time.Duration range (~292 years);
  // anything that long is not a threshold anyone means, so treat it the same.
  const maxMs = 292 * 365 * Duration.millisecondsPerDay;
  final value = raw.trim();
  if (value.isEmpty || value == '0') return null;
  final days = RegExp(r'^(\d+(?:\.\d+)?)d$').firstMatch(value);
  if (days != null) {
    final ms = double.parse(days.group(1)!) * Duration.millisecondsPerDay;
    if (ms <= 0 || ms > maxMs) return null;
    return Duration(milliseconds: ms.round());
  }
  final part = RegExp(r'(\d+(?:\.\d+)?)(h|ms|m|s)');
  var total = 0.0;
  var consumed = 0;
  for (final m in part.allMatches(value)) {
    if (m.start != consumed) return null;
    consumed = m.end;
    final n = double.parse(m.group(1)!);
    total += switch (m.group(2)) {
      'h' => n * Duration.millisecondsPerHour,
      'm' => n * Duration.millisecondsPerMinute,
      's' => n * Duration.millisecondsPerSecond,
      _ => n,
    };
  }
  if (consumed != value.length || total <= 0 || total > maxMs) return null;
  return Duration(milliseconds: total.round());
}

/// Review budget per rolling minute, hour and day (mirrors the daemon's
/// `review_limits` sections). 0 means "no limit" for that window.
class ReviewLimits {
  final int perMinute;
  final int perHour;
  final int perDay;

  const ReviewLimits({this.perMinute = 0, this.perHour = 0, this.perDay = 0});

  /// Upper bound the daemon accepts for every window (config.MaxReviewLimit).
  static const maxValue = 100000;

  bool get isEmpty => perMinute == 0 && perHour == 0 && perDay == 0;

  ReviewLimits copyWith({int? perMinute, int? perHour, int? perDay}) =>
      ReviewLimits(
        perMinute: perMinute ?? this.perMinute,
        perHour: perHour ?? this.perHour,
        perDay: perDay ?? this.perDay,
      );

  factory ReviewLimits.fromJson(Map<String, dynamic> json) => ReviewLimits(
    perMinute: (json['per_minute'] as num?)?.toInt() ?? 0,
    perHour: (json['per_hour'] as num?)?.toInt() ?? 0,
    perDay: (json['per_day'] as num?)?.toInt() ?? 0,
  );

  /// Parses an optional `review_limits` object; null when absent.
  static ReviewLimits? maybeFromJson(dynamic raw) =>
      raw is Map<String, dynamic> ? ReviewLimits.fromJson(raw) : null;

  Map<String, dynamic> toJson() => {
    'per_minute': perMinute,
    'per_hour': perHour,
    'per_day': perDay,
  };

  @override
  bool operator ==(Object other) =>
      other is ReviewLimits &&
      other.perMinute == perMinute &&
      other.perHour == perHour &&
      other.perDay == perDay;

  @override
  int get hashCode => Object.hash(perMinute, perHour, perDay);
}

/// Circuit-breaker rate limits for PR reviews.
class CircuitBreakerConfig {
  final int perPr24h;
  final int perRepoHr;

  // Defaults must stay in sync with the daemon's DefaultCircuitBreakerConfig()
  // in daemon/internal/config/circuit_breaker.go. They are only used as a
  // fallback when the daemon omits a key from GET /config.
  const CircuitBreakerConfig({this.perPr24h = 3, this.perRepoHr = 20});

  CircuitBreakerConfig copyWith({int? perPr24h, int? perRepoHr}) =>
      CircuitBreakerConfig(
        perPr24h: perPr24h ?? this.perPr24h,
        perRepoHr: perRepoHr ?? this.perRepoHr,
      );

  factory CircuitBreakerConfig.fromJson(Map<String, dynamic> json) =>
      CircuitBreakerConfig(
        perPr24h: (json['per_pr_24h'] as num?)?.toInt() ?? 3,
        perRepoHr: (json['per_repo_hr'] as num?)?.toInt() ?? 20,
      );

  Map<String, dynamic> toJson() => {
    'per_pr_24h': perPr24h,
    'per_repo_hr': perRepoHr,
  };
}

/// Polling / rate-limit configuration (mirrors [polling] TOML section).
class PollingConfig {
  final String pollInterval;
  final String discoveryInterval;
  final String tier3Interval;
  final int rateLimitSafetyThreshold;
  final bool useEtag;

  const PollingConfig({
    this.pollInterval = '',
    this.discoveryInterval = '5m',
    this.tier3Interval = '30s',
    this.rateLimitSafetyThreshold = 100,
    this.useEtag = true,
  });

  PollingConfig copyWith({
    String? pollInterval,
    String? discoveryInterval,
    String? tier3Interval,
    int? rateLimitSafetyThreshold,
    bool? useEtag,
  }) => PollingConfig(
    pollInterval: pollInterval ?? this.pollInterval,
    discoveryInterval: discoveryInterval ?? this.discoveryInterval,
    tier3Interval: tier3Interval ?? this.tier3Interval,
    rateLimitSafetyThreshold:
        rateLimitSafetyThreshold ?? this.rateLimitSafetyThreshold,
    useEtag: useEtag ?? this.useEtag,
  );

  factory PollingConfig.fromJson(Map<String, dynamic> json) => PollingConfig(
    pollInterval: (json['poll_interval'] as String?) ?? '',
    discoveryInterval: (json['discovery_interval'] as String?) ?? '5m',
    tier3Interval: (json['tier3_interval'] as String?) ?? '30s',
    rateLimitSafetyThreshold:
        ((json['rate_limit_safety_threshold'] as num?)?.toInt()) ?? 100,
    useEtag: (json['use_etag'] as bool?) ?? true,
  );

  Map<String, dynamic> toJson() => {
    'poll_interval': pollInterval,
    'discovery_interval': discoveryInterval,
    'tier3_interval': tier3Interval,
    'rate_limit_safety_threshold': rateLimitSafetyThreshold,
    'use_etag': useEtag,
  };
}

class AppConfig {
  final String? bindAddr;
  final int serverPort;
  final String pollInterval;
  final String aiPrimary;
  final String aiFallback;
  final String reviewMode; // "single" | "multi"
  final int retentionDays;
  final Map<String, CLIAgentConfig> agentConfigs; // keyed by CLI name
  final Map<String, RepoConfig> repoConfigs; // keyed by "org/repo"
  final Map<String, OrgConfig> orgConfigs; // keyed by "org"

  /// Bare org entries in `github.non_monitored` (no slash) — each one
  /// excludes every repo under that org, current and future, rather than
  /// naming a single repo (theburrowhub/heimdallm#828).
  final List<String> nonMonitoredOrgs;
  final String globalCloneDir;
  final bool globalNeverApproveWithIssues;

  /// Minimum finding severity that triggers the never-approve
  /// request-changes gate: 'low', 'medium' or 'high'. The daemon resolves an
  /// empty value to [defaultNeverApproveMinSeverity], so the UI seeds the
  /// dropdown with that same default rather than showing a blank selection.
  final String globalNeverApproveMinSeverity;

  final MergeTrackingConfig mergeTracking;
  final MyPrsConfig myPrs;
  final CircuitBreakerConfig circuitBreaker;

  /// Global review budget (`[review_limits]`).
  final ReviewLimits reviewLimits;
  final PollingConfig polling;

  /// Host paths the daemon scans (in order) when a repo has no explicit
  /// `local_dir` set — first match at `{base}/{short-repo-name}` wins.
  final List<String> localDirBase;

  /// Auto-detected `local_dir` per repo, populated by the daemon when the
  /// repo is visible at `/home/heimdallm/repos/<short-name>` in the
  /// container (i.e. the operator set HEIMDALLM_LOCAL_DIR_BASE). The
  /// daemon falls back to this
  /// value at review time when the per-repo `local_dir` is empty; the UI
  /// surfaces it next to the repo so the user knows full-repo analysis
  /// will kick in without configuring anything. Keyed by "org/repo".
  final Map<String, String> localDirsDetected;

  /// This daemon's cluster role — 'standalone' | 'hub' | 'worker' (see
  /// [ClusterRole]). Changing it only takes effect after a daemon restart:
  /// the control plane (SetCluster/SetClusterIdentity, the health prober) is
  /// wired once at startup, never on config reload.
  final String clusterRole;

  const AppConfig({
    this.bindAddr,
    this.serverPort = 7842,
    this.pollInterval = '5m',
    this.aiPrimary = 'claude',
    this.aiFallback = '',
    this.reviewMode = 'single',
    this.retentionDays = 90,
    this.agentConfigs = const {},
    this.repoConfigs = const {},
    this.orgConfigs = const {},
    this.nonMonitoredOrgs = const [],
    this.globalCloneDir = '',
    this.mergeTracking = const MergeTrackingConfig(),
    this.myPrs = const MyPrsConfig(),
    this.circuitBreaker = const CircuitBreakerConfig(),
    this.reviewLimits = const ReviewLimits(),
    this.globalNeverApproveWithIssues = false,
    this.globalNeverApproveMinSeverity = defaultNeverApproveMinSeverity,
    this.polling = const PollingConfig(),
    this.localDirBase = const [],
    this.localDirsDetected = const {},
    this.clusterRole = ClusterRole.standalone,
  });

  /// Computed list of monitored repos — this is what the daemon uses.
  /// A repo is monitored if any of its features is active.
  List<String> get repositories =>
      (repoConfigs.entries
          .where((e) => e.value.isMonitored)
          .map((e) => e.key)
          .toList()
        ..sort());

  List<String> get knownOrganizations {
    final orgs = <String>{
      ...orgConfigs.keys,
      ...mergeTracking.orgs.keys,
      ...nonMonitoredOrgs,
    };
    for (final repo in repoConfigs.keys) {
      final slash = repo.indexOf('/');
      if (slash > 0) orgs.add(repo.substring(0, slash));
    }
    return orgs.where((o) => o.trim().isNotEmpty).toList()..sort();
  }

  /// Whether [org] has a bare entry in `non_monitored` (theburrowhub/heimdallm#828).
  /// Case-insensitive — GitHub org/user names are, matching the daemon's
  /// discovery.BuildNonMonitoredOrgs.
  bool isOrgNonMonitored(String org) {
    final lower = org.toLowerCase();
    return nonMonitoredOrgs.any((o) => o.toLowerCase() == lower);
  }

  /// The raw `github.non_monitored` list as the daemon expects it back on a
  /// `patchConfig` write: bare org entries first, then exact "owner/repo"
  /// entries for individually-disabled repos. Any writer that mutates one
  /// half (an org toggle, a per-repo toggle) must source the other half from
  /// here so it doesn't silently drop the entries it isn't changing.
  List<String> get nonMonitoredList =>
      [
          ...nonMonitoredOrgs,
          ...repoConfigs.entries
              .where((e) => !e.value.isMonitored)
              .map((e) => e.key),
        ]
        ..sort();

  AppConfig copyWith({
    Object? bindAddr = _sentinel,
    int? serverPort,
    String? pollInterval,
    String? aiPrimary,
    String? aiFallback,
    String? reviewMode,
    int? retentionDays,
    Map<String, CLIAgentConfig>? agentConfigs,
    Map<String, RepoConfig>? repoConfigs,
    Map<String, OrgConfig>? orgConfigs,
    List<String>? nonMonitoredOrgs,
    String? globalCloneDir,
    MergeTrackingConfig? mergeTracking,
    MyPrsConfig? myPrs,
    CircuitBreakerConfig? circuitBreaker,
    ReviewLimits? reviewLimits,
    bool? globalNeverApproveWithIssues,
    String? globalNeverApproveMinSeverity,
    PollingConfig? polling,
    List<String>? localDirBase,
    Map<String, String>? localDirsDetected,
    String? clusterRole,
  }) {
    return AppConfig(
      bindAddr: bindAddr == _sentinel ? this.bindAddr : bindAddr as String?,
      serverPort: serverPort ?? this.serverPort,
      pollInterval: pollInterval ?? this.pollInterval,
      aiPrimary: aiPrimary ?? this.aiPrimary,
      aiFallback: aiFallback ?? this.aiFallback,
      reviewMode: reviewMode ?? this.reviewMode,
      retentionDays: retentionDays ?? this.retentionDays,
      agentConfigs: agentConfigs ?? this.agentConfigs,
      repoConfigs: repoConfigs ?? this.repoConfigs,
      orgConfigs: orgConfigs ?? this.orgConfigs,
      nonMonitoredOrgs: nonMonitoredOrgs ?? this.nonMonitoredOrgs,
      globalCloneDir: globalCloneDir ?? this.globalCloneDir,
      mergeTracking: mergeTracking ?? this.mergeTracking,
      myPrs: myPrs ?? this.myPrs,
      circuitBreaker: circuitBreaker ?? this.circuitBreaker,
      reviewLimits: reviewLimits ?? this.reviewLimits,
      globalNeverApproveWithIssues:
          globalNeverApproveWithIssues ?? this.globalNeverApproveWithIssues,
      globalNeverApproveMinSeverity:
          globalNeverApproveMinSeverity ?? this.globalNeverApproveMinSeverity,
      polling: polling ?? this.polling,
      localDirBase: localDirBase ?? this.localDirBase,
      localDirsDetected: localDirsDetected ?? this.localDirsDetected,
      clusterRole: clusterRole ?? this.clusterRole,
    );
  }

  Map<String, dynamic> toJson() => {
    if (bindAddr != null) 'bind_addr': bindAddr,
    'server_port': serverPort,
    'cluster': {'role': clusterRole},
    'poll_interval': pollInterval,
    'repositories': repositories,
    'ai_primary': aiPrimary,
    'ai_fallback': aiFallback,
    'review_mode': reviewMode,
    'retention_days': retentionDays,
    'clone_dir': globalCloneDir,
    'never_approve_with_issues': globalNeverApproveWithIssues,
    'never_approve_min_severity': globalNeverApproveMinSeverity,
  };

  factory AppConfig.fromJson(Map<String, dynamic> json) {
    final repos =
        (json['repositories'] as List<dynamic>?)?.cast<String>() ?? [];
    final configs = <String, RepoConfig>{
      // Repos in the monitored list have PR review enabled
      for (final r in repos) r: const RepoConfig(prEnabled: true),
    };
    // Restore non-monitored repos. A bare entry (no slash) excludes a whole
    // org (theburrowhub/heimdallm#828) rather than naming one repo, so it
    // must not become a fake RepoConfig keyed by the org name.
    final nonMonitored =
        (json['non_monitored'] as List<dynamic>?)?.cast<String>() ?? [];
    final nonMonitoredOrgs = <String>[];
    for (final r in nonMonitored) {
      if (r.contains('/')) {
        configs.putIfAbsent(r, () => const RepoConfig());
      } else if (r.isNotEmpty) {
        nonMonitoredOrgs.add(r);
      }
    }
    // Per-repo overrides (normalize empty strings to null)
    final overrides = json['repo_overrides'] as Map<String, dynamic>?;
    if (overrides != null) {
      for (final entry in overrides.entries) {
        final ov = entry.value as Map<String, dynamic>;
        final existing = configs[entry.key];
        final fsRaw = ov['first_seen_at'];
        final firstSeen = fsRaw is int
            ? DateTime.fromMillisecondsSinceEpoch(fsRaw * 1000)
            : null;
        configs[entry.key] = RepoConfig(
          prEnabled: existing?.prEnabled,
          localDir: _nonEmpty(ov['local_dir']),
          cloneDir: _nonEmpty(ov['clone_dir']),
          aiPrimary: _nonEmpty(ov['primary']),
          aiFallback: _nonEmpty(ov['fallback']),
          reviewMode: _nonEmpty(ov['review_mode']),
          promptId: _nonEmpty(ov['prompt']),
          neverApproveWithIssues: ov['never_approve_with_issues'] as bool?,
          neverApproveMinSeverity: _nonEmpty(ov['never_approve_min_severity']),
          reviewLimits: ReviewLimits.maybeFromJson(ov['review_limits']),
          firstSeenAt: firstSeen,
        );
      }
    }
    // Flag repos whose org is bare-excluded (theburrowhub/heimdallm#828) so
    // the UI can show them as not-really-monitored without touching
    // isMonitored (which still drives what gets written to
    // github.repositories).
    if (nonMonitoredOrgs.isNotEmpty) {
      // GitHub org/user names are case-insensitive, matching the daemon's
      // discovery.BuildNonMonitoredOrgs.
      final orgSet = nonMonitoredOrgs.map((o) => o.toLowerCase()).toSet();
      for (final key in configs.keys.toList()) {
        final slash = key.indexOf('/');
        if (slash <= 0) continue;
        if (orgSet.contains(key.substring(0, slash).toLowerCase())) {
          configs[key] = configs[key]!.copyWith(excludedByOrg: true);
        }
      }
    }
    final orgOverrides = json['org_overrides'] as Map<String, dynamic>?;
    final orgConfigs = <String, OrgConfig>{};
    if (orgOverrides != null) {
      for (final entry in orgOverrides.entries) {
        orgConfigs[entry.key] = OrgConfig.fromJson(
          entry.value as Map<String, dynamic>,
        );
      }
    }
    // Agent configs
    final agentsRaw = json['agent_configs'] as Map<String, dynamic>?;
    final agentConfigs = <String, CLIAgentConfig>{};
    if (agentsRaw != null) {
      for (final entry in agentsRaw.entries) {
        agentConfigs[entry.key] = CLIAgentConfig.fromJson(
          entry.value as Map<String, dynamic>,
        );
      }
    }
    // Auto-detected local_dir map (may be absent on older daemons).
    final detectedRaw = json['local_dirs_detected'] as Map<String, dynamic>?;
    final localDirsDetected = <String, String>{};
    if (detectedRaw != null) {
      for (final entry in detectedRaw.entries) {
        final v = entry.value;
        if (v is String && v.isNotEmpty) localDirsDetected[entry.key] = v;
      }
    }

    return AppConfig(
      bindAddr: json['bind_addr'] as String?,
      serverPort: (json['server_port'] as int?) ?? 7842,
      pollInterval: (json['poll_interval'] as String?) ?? '5m',
      aiPrimary: (json['ai_primary'] as String?) ?? 'claude',
      aiFallback: (json['ai_fallback'] as String?) ?? '',
      reviewMode: (json['review_mode'] as String?) ?? 'single',
      retentionDays: (json['retention_days'] as int?) ?? 90,
      agentConfigs: agentConfigs,
      repoConfigs: _withMergeTrackingOverrides(configs, json),
      orgConfigs: _withMergeTrackingOrgOverrides(orgConfigs, json),
      nonMonitoredOrgs: nonMonitoredOrgs..sort(),
      globalCloneDir: (json['clone_dir'] as String?) ?? '',
      mergeTracking: json['merge_tracking'] != null
          ? MergeTrackingConfig.fromJson(
              json['merge_tracking'] as Map<String, dynamic>,
            )
          : const MergeTrackingConfig(),
      myPrs: json['my_prs'] is Map<String, dynamic>
          ? MyPrsConfig.fromJson(json['my_prs'] as Map<String, dynamic>)
          : const MyPrsConfig(),
      circuitBreaker: json['circuit_breaker'] != null
          ? CircuitBreakerConfig.fromJson(
              json['circuit_breaker'] as Map<String, dynamic>,
            )
          : const CircuitBreakerConfig(),
      reviewLimits:
          ReviewLimits.maybeFromJson(json['review_limits']) ??
          const ReviewLimits(),
      globalNeverApproveWithIssues:
          (json['never_approve_with_issues'] as bool?) ?? false,
      // The daemon serves "" when unset; surface the default it will actually
      // apply so the dropdown never renders an out-of-range empty value.
      globalNeverApproveMinSeverity:
          _nonEmpty(json['never_approve_min_severity']) ??
          defaultNeverApproveMinSeverity,
      polling: json['polling'] != null
          ? PollingConfig.fromJson(json['polling'] as Map<String, dynamic>)
          : const PollingConfig(),
      localDirBase: _parseStringList(json['local_dir_base']),
      localDirsDetected: localDirsDetected,
      // Defensive against an older daemon that omits `cluster` entirely, or
      // one that reports an empty role: both mean standalone.
      clusterRole:
          _nonEmpty((json['cluster'] as Map<String, dynamic>?)?['role']) ??
          ClusterRole.standalone,
    );
  }

  static List<String> _parseStringList(dynamic v) {
    if (v is List) return v.cast<String>();
    return const [];
  }
}

/// Folds `merge_tracking.repos.<repo>` into each RepoConfig.
///
/// Merge tracking keeps its overrides in its own config section rather than in
/// `repo_overrides`, so without this the scoped editor would have nothing to
/// read and the LED would always render inherited.
Map<String, RepoConfig> _withMergeTrackingOverrides(
  Map<String, RepoConfig> configs,
  Map<String, dynamic> json,
) {
  final repos =
      (json['merge_tracking'] as Map<String, dynamic>?)?['repos']
          as Map<String, dynamic>?;
  if (repos == null || repos.isEmpty) return configs;

  final out = Map<String, RepoConfig>.from(configs);
  for (final entry in repos.entries) {
    final raw = entry.value;
    if (raw is! Map<String, dynamic>) continue;
    final existing = out[entry.key];
    // This map also drives github.repositories/non_monitored writes. An
    // override alone is not proof that the daemon considers the repo part of
    // either list, so never synthesize membership from merge_tracking.repos.
    if (existing == null) continue;
    out[entry.key] = existing.copyWith(
      mergeTracking: MergeTrackingOverride.fromJson(raw),
    );
  }
  return out;
}

/// The org-level half of [_withMergeTrackingOverrides].
Map<String, OrgConfig> _withMergeTrackingOrgOverrides(
  Map<String, OrgConfig> configs,
  Map<String, dynamic> json,
) {
  final orgs =
      (json['merge_tracking'] as Map<String, dynamic>?)?['orgs']
          as Map<String, dynamic>?;
  if (orgs == null || orgs.isEmpty) return configs;

  final out = Map<String, OrgConfig>.from(configs);
  for (final entry in orgs.entries) {
    final raw = entry.value;
    if (raw is! Map<String, dynamic>) continue;
    out[entry.key] = (out[entry.key] ?? const OrgConfig()).copyWith(
      mergeTracking: MergeTrackingOverride.fromJson(raw),
    );
  }
  return out;
}
