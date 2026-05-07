/// Per-agent CLI execution settings.
/// Stored under ai.agents.<name> in config.toml.
class CLIAgentConfig {
  final String model; // --model value ('' = use CLI default)
  final int maxTurns; // claude: --max-turns (0 = not set)
  final String approvalMode; // codex: --approval-mode ('' = not set)
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
      noSessionPersistence;

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
  );

  static const modelOptions = <String, List<String>>{
    'claude': [
      'claude-opus-4-6',
      'claude-sonnet-4-6',
      'claude-haiku-4-5-20251001',
    ],
    'gemini': ['gemini-2.5-pro', 'gemini-2.0-flash', 'gemini-1.5-pro'],
    'codex': ['o4-mini', 'o3', 'gpt-4o'],
  };

  static const approvalModeOptions = ['full-auto', 'auto-edit', 'suggest'];
  static const effortOptions = ['low', 'medium', 'high', 'max'];
  static const permissionModeOptions = [
    'default',
    'auto',
    'bypassPermissions',
    'acceptEdits',
    'dontAsk',
  ];
}

/// Per-repo AI override. null fields mean "use global default".
class RepoConfig {
  // Per-feature activation (null = inherit global behavior)
  final bool? prEnabled; // PR auto-review
  final bool? itEnabled; // Issue tracking (triage)
  final bool? devEnabled; // Develop (auto-implement)

  // General
  final String? localDir; // local repo directory for full-repo analysis
  final String? triageOwner;
  final String? cloneDir;
  final bool? autoPromoteTriage;
  final bool? autoPromoteRefinement;
  final bool? generatePRDescription;
  final DateTime?
  firstSeenAt; // when the daemon first discovered this repo (null = unknown)

  // PR Review config
  final String? aiPrimary; // null = use global
  final String? aiFallback; // null = use global
  final String? promptId; // null = use globally active prompt
  final String? reviewMode; // null = use global ("single" | "multi")

  // Issue tracking overrides (null = inherit global)
  final List<String>? reviewOnlyLabels;
  final List<String>? skipLabels;
  final String? issueFilterMode;
  final String? issueDefaultAction;
  final List<String>? issueOrganizations;
  final List<String>? issueAssignees;
  final String? issuePromptId;

  // Develop overrides (null = inherit global)
  final List<String>? developLabels;
  final String? developPromptId;

  // PR metadata applied after auto_implement creates a PR
  final List<String>? prReviewers;
  final String? prAssignee;
  final List<String>? prLabels;
  final bool? prDraft;

  const RepoConfig({
    this.prEnabled,
    this.itEnabled,
    this.devEnabled,
    this.localDir,
    this.triageOwner,
    this.cloneDir,
    this.autoPromoteTriage,
    this.autoPromoteRefinement,
    this.generatePRDescription,
    this.aiPrimary,
    this.aiFallback,
    this.promptId,
    this.reviewMode,
    this.reviewOnlyLabels,
    this.skipLabels,
    this.issueFilterMode,
    this.issueDefaultAction,
    this.issueOrganizations,
    this.issueAssignees,
    this.issuePromptId,
    this.developLabels,
    this.developPromptId,
    this.prReviewers,
    this.prAssignee,
    this.prLabels,
    this.prDraft,
    this.firstSeenAt,
  });

  /// True if any feature is actively enabled (per-repo or inherited).
  /// Used by the repo list to classify monitored vs not-monitored,
  /// and by the TOML writer to decide which repos go in `repositories`.
  bool get isMonitored {
    final issueActive =
        itEnabled == true ||
        (itEnabled != false &&
            reviewOnlyLabels != null &&
            reviewOnlyLabels!.isNotEmpty);
    final developActive =
        devEnabled == true ||
        (devEnabled != false &&
            developLabels != null &&
            developLabels!.isNotEmpty);
    return (prEnabled ?? false) || issueActive || developActive;
  }

  /// Legacy getter — repos with any override need to be written to TOML.
  bool get hasAiOverride =>
      prEnabled != null ||
      itEnabled != null ||
      devEnabled != null ||
      aiPrimary != null ||
      aiFallback != null ||
      promptId != null ||
      reviewMode != null ||
      (localDir != null && localDir!.isNotEmpty) ||
      triageOwner != null ||
      cloneDir != null ||
      autoPromoteTriage != null ||
      autoPromoteRefinement != null ||
      generatePRDescription != null ||
      developLabels != null ||
      reviewOnlyLabels != null ||
      skipLabels != null ||
      issueFilterMode != null ||
      issueDefaultAction != null ||
      issueOrganizations != null ||
      issueAssignees != null ||
      issuePromptId != null ||
      developPromptId != null ||
      prReviewers != null ||
      prAssignee != null ||
      prLabels != null ||
      prDraft != null;

  /// LED status for each feature: 'off', 'global', 'repo'
  String prLedStatus(bool globalMonitored) {
    if (prEnabled == true) return 'repo';
    if (prEnabled == false) return 'off';
    return globalMonitored ? 'global' : 'off';
  }

  String itLedStatus(bool globalITEnabled) {
    // Explicit toggle wins
    if (itEnabled == true) return 'repo';
    if (itEnabled == false) return 'off';
    // Labels configured = implicitly active (matches daemon behavior)
    if (reviewOnlyLabels != null && reviewOnlyLabels!.isNotEmpty) return 'repo';
    return globalITEnabled ? 'global' : 'off';
  }

  String devLedStatus(bool globalITEnabled, bool hasLocalDir) {
    if (devEnabled == true) return 'repo';
    if (devEnabled == false) return 'off';
    // Labels configured = implicitly active
    if (developLabels != null && developLabels!.isNotEmpty && hasLocalDir) {
      return 'repo';
    }
    return (globalITEnabled && hasLocalDir) ? 'global' : 'off';
  }

  RepoConfig copyWith({
    Object? prEnabled = _sentinel,
    Object? itEnabled = _sentinel,
    Object? devEnabled = _sentinel,
    Object? localDir = _sentinel,
    Object? triageOwner = _sentinel,
    Object? cloneDir = _sentinel,
    Object? autoPromoteTriage = _sentinel,
    Object? autoPromoteRefinement = _sentinel,
    Object? generatePRDescription = _sentinel,
    Object? aiPrimary = _sentinel,
    Object? aiFallback = _sentinel,
    Object? promptId = _sentinel,
    Object? reviewMode = _sentinel,
    Object? reviewOnlyLabels = _sentinel,
    Object? skipLabels = _sentinel,
    Object? issueFilterMode = _sentinel,
    Object? issueDefaultAction = _sentinel,
    Object? issueOrganizations = _sentinel,
    Object? issueAssignees = _sentinel,
    Object? issuePromptId = _sentinel,
    Object? developLabels = _sentinel,
    Object? developPromptId = _sentinel,
    Object? prReviewers = _sentinel,
    Object? prAssignee = _sentinel,
    Object? prLabels = _sentinel,
    Object? prDraft = _sentinel,
    Object? firstSeenAt = _sentinel,
  }) {
    return RepoConfig(
      prEnabled: prEnabled == _sentinel ? this.prEnabled : prEnabled as bool?,
      itEnabled: itEnabled == _sentinel ? this.itEnabled : itEnabled as bool?,
      devEnabled: devEnabled == _sentinel
          ? this.devEnabled
          : devEnabled as bool?,
      localDir: localDir == _sentinel ? this.localDir : localDir as String?,
      triageOwner: triageOwner == _sentinel
          ? this.triageOwner
          : triageOwner as String?,
      cloneDir: cloneDir == _sentinel ? this.cloneDir : cloneDir as String?,
      autoPromoteTriage: autoPromoteTriage == _sentinel
          ? this.autoPromoteTriage
          : autoPromoteTriage as bool?,
      autoPromoteRefinement: autoPromoteRefinement == _sentinel
          ? this.autoPromoteRefinement
          : autoPromoteRefinement as bool?,
      generatePRDescription: generatePRDescription == _sentinel
          ? this.generatePRDescription
          : generatePRDescription as bool?,
      aiPrimary: aiPrimary == _sentinel ? this.aiPrimary : aiPrimary as String?,
      aiFallback: aiFallback == _sentinel
          ? this.aiFallback
          : aiFallback as String?,
      promptId: promptId == _sentinel ? this.promptId : promptId as String?,
      reviewMode: reviewMode == _sentinel
          ? this.reviewMode
          : reviewMode as String?,
      reviewOnlyLabels: reviewOnlyLabels == _sentinel
          ? this.reviewOnlyLabels
          : reviewOnlyLabels as List<String>?,
      skipLabels: skipLabels == _sentinel
          ? this.skipLabels
          : skipLabels as List<String>?,
      issueFilterMode: issueFilterMode == _sentinel
          ? this.issueFilterMode
          : issueFilterMode as String?,
      issueDefaultAction: issueDefaultAction == _sentinel
          ? this.issueDefaultAction
          : issueDefaultAction as String?,
      issueOrganizations: issueOrganizations == _sentinel
          ? this.issueOrganizations
          : issueOrganizations as List<String>?,
      issueAssignees: issueAssignees == _sentinel
          ? this.issueAssignees
          : issueAssignees as List<String>?,
      issuePromptId: issuePromptId == _sentinel
          ? this.issuePromptId
          : issuePromptId as String?,
      developLabels: developLabels == _sentinel
          ? this.developLabels
          : developLabels as List<String>?,
      developPromptId: developPromptId == _sentinel
          ? this.developPromptId
          : developPromptId as String?,
      prReviewers: prReviewers == _sentinel
          ? this.prReviewers
          : prReviewers as List<String>?,
      prAssignee: prAssignee == _sentinel
          ? this.prAssignee
          : prAssignee as String?,
      prLabels: prLabels == _sentinel
          ? this.prLabels
          : prLabels as List<String>?,
      prDraft: prDraft == _sentinel ? this.prDraft : prDraft as bool?,
      firstSeenAt: firstSeenAt == _sentinel
          ? this.firstSeenAt
          : firstSeenAt as DateTime?,
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
  final String? triageOwner;
  final String? cloneDir;
  final bool? autoPromoteTriage;
  final bool? autoPromoteRefinement;
  final bool? generatePRDescription;
  final String? issuePromptId;
  final String? developPromptId;
  final bool? itEnabled;
  final bool? devEnabled;
  final List<String>? reviewOnlyLabels;
  final List<String>? developLabels;
  final List<String>? skipLabels;
  final String? issueFilterMode;
  final String? issueDefaultAction;
  final List<String>? issueOrganizations;
  final List<String>? issueAssignees;
  final List<String>? prReviewers;
  final String? prAssignee;
  final List<String>? prLabels;
  final bool? prDraft;

  const OrgConfig({
    this.aiPrimary,
    this.aiFallback,
    this.promptId,
    this.reviewMode,
    this.localDir,
    this.triageOwner,
    this.cloneDir,
    this.autoPromoteTriage,
    this.autoPromoteRefinement,
    this.generatePRDescription,
    this.issuePromptId,
    this.developPromptId,
    this.itEnabled,
    this.devEnabled,
    this.reviewOnlyLabels,
    this.developLabels,
    this.skipLabels,
    this.issueFilterMode,
    this.issueDefaultAction,
    this.issueOrganizations,
    this.issueAssignees,
    this.prReviewers,
    this.prAssignee,
    this.prLabels,
    this.prDraft,
  });

  bool get hasOverride =>
      aiPrimary != null ||
      aiFallback != null ||
      promptId != null ||
      reviewMode != null ||
      localDir != null ||
      triageOwner != null ||
      cloneDir != null ||
      autoPromoteTriage != null ||
      autoPromoteRefinement != null ||
      generatePRDescription != null ||
      issuePromptId != null ||
      developPromptId != null ||
      itEnabled != null ||
      devEnabled != null ||
      reviewOnlyLabels != null ||
      developLabels != null ||
      skipLabels != null ||
      issueFilterMode != null ||
      issueDefaultAction != null ||
      issueOrganizations != null ||
      issueAssignees != null ||
      prReviewers != null ||
      prAssignee != null ||
      prLabels != null ||
      prDraft != null;

  OrgConfig copyWith({
    Object? aiPrimary = _sentinel,
    Object? aiFallback = _sentinel,
    Object? promptId = _sentinel,
    Object? reviewMode = _sentinel,
    Object? localDir = _sentinel,
    Object? triageOwner = _sentinel,
    Object? cloneDir = _sentinel,
    Object? autoPromoteTriage = _sentinel,
    Object? autoPromoteRefinement = _sentinel,
    Object? generatePRDescription = _sentinel,
    Object? issuePromptId = _sentinel,
    Object? developPromptId = _sentinel,
    Object? itEnabled = _sentinel,
    Object? devEnabled = _sentinel,
    Object? reviewOnlyLabels = _sentinel,
    Object? developLabels = _sentinel,
    Object? skipLabels = _sentinel,
    Object? issueFilterMode = _sentinel,
    Object? issueDefaultAction = _sentinel,
    Object? issueOrganizations = _sentinel,
    Object? issueAssignees = _sentinel,
    Object? prReviewers = _sentinel,
    Object? prAssignee = _sentinel,
    Object? prLabels = _sentinel,
    Object? prDraft = _sentinel,
  }) => OrgConfig(
    aiPrimary: aiPrimary == _sentinel ? this.aiPrimary : aiPrimary as String?,
    aiFallback: aiFallback == _sentinel
        ? this.aiFallback
        : aiFallback as String?,
    promptId: promptId == _sentinel ? this.promptId : promptId as String?,
    reviewMode: reviewMode == _sentinel
        ? this.reviewMode
        : reviewMode as String?,
    localDir: localDir == _sentinel ? this.localDir : localDir as String?,
    triageOwner: triageOwner == _sentinel
        ? this.triageOwner
        : triageOwner as String?,
    cloneDir: cloneDir == _sentinel ? this.cloneDir : cloneDir as String?,
    autoPromoteTriage: autoPromoteTriage == _sentinel
        ? this.autoPromoteTriage
        : autoPromoteTriage as bool?,
    autoPromoteRefinement: autoPromoteRefinement == _sentinel
        ? this.autoPromoteRefinement
        : autoPromoteRefinement as bool?,
    generatePRDescription: generatePRDescription == _sentinel
        ? this.generatePRDescription
        : generatePRDescription as bool?,
    issuePromptId: issuePromptId == _sentinel
        ? this.issuePromptId
        : issuePromptId as String?,
    developPromptId: developPromptId == _sentinel
        ? this.developPromptId
        : developPromptId as String?,
    itEnabled: itEnabled == _sentinel ? this.itEnabled : itEnabled as bool?,
    devEnabled: devEnabled == _sentinel ? this.devEnabled : devEnabled as bool?,
    reviewOnlyLabels: reviewOnlyLabels == _sentinel
        ? this.reviewOnlyLabels
        : reviewOnlyLabels as List<String>?,
    developLabels: developLabels == _sentinel
        ? this.developLabels
        : developLabels as List<String>?,
    skipLabels: skipLabels == _sentinel
        ? this.skipLabels
        : skipLabels as List<String>?,
    issueFilterMode: issueFilterMode == _sentinel
        ? this.issueFilterMode
        : issueFilterMode as String?,
    issueDefaultAction: issueDefaultAction == _sentinel
        ? this.issueDefaultAction
        : issueDefaultAction as String?,
    issueOrganizations: issueOrganizations == _sentinel
        ? this.issueOrganizations
        : issueOrganizations as List<String>?,
    issueAssignees: issueAssignees == _sentinel
        ? this.issueAssignees
        : issueAssignees as List<String>?,
    prReviewers: prReviewers == _sentinel
        ? this.prReviewers
        : prReviewers as List<String>?,
    prAssignee: prAssignee == _sentinel
        ? this.prAssignee
        : prAssignee as String?,
    prLabels: prLabels == _sentinel ? this.prLabels : prLabels as List<String>?,
    prDraft: prDraft == _sentinel ? this.prDraft : prDraft as bool?,
  );

  factory OrgConfig.fromJson(Map<String, dynamic> json) {
    final itRaw = json['issue_tracking'] as Map<String, dynamic>?;
    final hasReviewLabels =
        _nullableStringList(itRaw?['review_only_labels']) != null;
    final hasDevLabels = _nullableStringList(itRaw?['develop_labels']) != null;
    final itExplicit = itRaw != null && itRaw.containsKey('enabled')
        ? itRaw['enabled'] as bool?
        : null;
    final devExplicit = itRaw != null && itRaw.containsKey('develop_enabled')
        ? itRaw['develop_enabled'] as bool?
        : null;
    return OrgConfig(
      aiPrimary: _nonEmpty(json['primary']),
      aiFallback: _nonEmpty(json['fallback']),
      promptId: _nonEmpty(json['prompt']),
      reviewMode: _nonEmpty(json['review_mode']),
      localDir: _nonEmpty(json['local_dir']),
      triageOwner: _nonEmpty(json['triage_owner']),
      cloneDir: _nonEmpty(json['clone_dir']),
      autoPromoteTriage: json['auto_promote_triage'] as bool?,
      autoPromoteRefinement: json['auto_promote_refinement'] as bool?,
      generatePRDescription: json['generate_pr_description'] as bool?,
      issuePromptId:
          _nonEmpty(json['issue_prompt']) ??
          (itRaw != null ? _nonEmpty(itRaw['issue_prompt']) : null),
      developPromptId: _nonEmpty(json['implement_prompt']),
      itEnabled: itExplicit ?? (hasReviewLabels ? true : null),
      devEnabled: devExplicit ?? (hasDevLabels ? true : null),
      reviewOnlyLabels: itRaw != null
          ? _nullableStringListAllowEmpty(itRaw['review_only_labels'])
          : null,
      developLabels: itRaw != null
          ? _nullableStringListAllowEmpty(itRaw['develop_labels'])
          : null,
      skipLabels: itRaw != null
          ? _nullableStringListAllowEmpty(itRaw['skip_labels'])
          : null,
      issueFilterMode: itRaw != null ? _nonEmpty(itRaw['filter_mode']) : null,
      issueDefaultAction: itRaw != null
          ? _nonEmpty(itRaw['default_action'])
          : null,
      issueOrganizations: itRaw != null
          ? _nullableStringListAllowEmpty(itRaw['organizations'])
          : null,
      issueAssignees: itRaw != null
          ? _nullableStringListAllowEmpty(itRaw['assignees'])
          : null,
      prReviewers: _nullableStringListAllowEmpty(json['pr_reviewers']),
      prAssignee: _nonEmpty(json['pr_assignee']),
      prLabels: _nullableStringListAllowEmpty(json['pr_labels']),
      prDraft: json['pr_draft'] as bool?,
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

/// Returns null when the list is absent or empty, otherwise a non-empty String list.
List<String>? _nullableStringList(dynamic v) {
  final list = (v as List<dynamic>?)
      ?.cast<String>()
      .where((s) => s.isNotEmpty)
      .toList();
  return (list != null && list.isNotEmpty) ? list : null;
}

/// Returns null when absent, but preserves an explicit empty list override.
List<String>? _nullableStringListAllowEmpty(dynamic v) {
  if (v == null) return null;
  return (v as List<dynamic>)
      .cast<String>()
      .where((s) => s.isNotEmpty)
      .toList();
}

/// Issue tracking pipeline configuration.
class IssueTrackingConfig {
  final bool enabled;
  final String filterMode; // "exclusive" | "inclusive"
  final String defaultAction; // "ignore" | "review_only"
  final List<String> developLabels;
  final List<String> reviewOnlyLabels;
  final List<String> skipLabels;
  final List<String> organizations;
  final List<String> assignees;

  const IssueTrackingConfig({
    this.enabled = false,
    this.filterMode = 'exclusive',
    this.defaultAction = 'ignore',
    this.developLabels = const [],
    this.reviewOnlyLabels = const [],
    this.skipLabels = const [],
    this.organizations = const [],
    this.assignees = const [],
  });

  IssueTrackingConfig copyWith({
    bool? enabled,
    String? filterMode,
    String? defaultAction,
    List<String>? developLabels,
    List<String>? reviewOnlyLabels,
    List<String>? skipLabels,
    List<String>? organizations,
    List<String>? assignees,
  }) => IssueTrackingConfig(
    enabled: enabled ?? this.enabled,
    filterMode: filterMode ?? this.filterMode,
    defaultAction: defaultAction ?? this.defaultAction,
    developLabels: developLabels ?? this.developLabels,
    reviewOnlyLabels: reviewOnlyLabels ?? this.reviewOnlyLabels,
    skipLabels: skipLabels ?? this.skipLabels,
    organizations: organizations ?? this.organizations,
    assignees: assignees ?? this.assignees,
  );

  Map<String, dynamic> toJson() => {
    'enabled': enabled,
    'filter_mode': filterMode,
    'default_action': defaultAction,
    'develop_labels': developLabels,
    'review_only_labels': reviewOnlyLabels,
    'skip_labels': skipLabels,
    'organizations': organizations,
    'assignees': assignees,
  };

  static const validFilterModes = ['exclusive', 'inclusive'];
  static const validDefaultActions = ['ignore', 'review_only'];

  factory IssueTrackingConfig.fromJson(Map<String, dynamic> json) {
    final rawFilterMode = (json['filter_mode'] as String?) ?? 'exclusive';
    final rawDefaultAction = (json['default_action'] as String?) ?? 'ignore';
    return IssueTrackingConfig(
      enabled: (json['enabled'] as bool?) ?? false,
      filterMode: validFilterModes.contains(rawFilterMode)
          ? rawFilterMode
          : 'exclusive',
      defaultAction: validDefaultActions.contains(rawDefaultAction)
          ? rawDefaultAction
          : 'ignore',
      developLabels: _stringList(json['develop_labels']),
      reviewOnlyLabels: _stringList(json['review_only_labels']),
      skipLabels: _stringList(json['skip_labels']),
      organizations: _stringList(json['organizations']),
      assignees: _stringList(json['assignees']),
    );
  }

  static List<String> _stringList(dynamic v) =>
      (v as List<dynamic>?)?.cast<String>() ?? [];
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
  final IssueTrackingConfig issueTracking;
  final List<String> globalPRReviewers;
  final List<String> globalPRLabels;
  final String globalPRAssignee;
  final bool globalPRDraft;
  final String globalIssuePrompt;
  final String globalImplementPrompt;
  final String globalTriageOwner;
  final String globalCloneDir;
  final bool? globalAutoPromoteTriage;
  final bool? globalAutoPromoteRefinement;
  final bool globalGeneratePRDescription;

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
    this.issueTracking = const IssueTrackingConfig(),
    this.globalPRReviewers = const [],
    this.globalPRLabels = const [],
    this.globalPRAssignee = '',
    this.globalPRDraft = false,
    this.globalIssuePrompt = '',
    this.globalImplementPrompt = '',
    this.globalTriageOwner = '',
    this.globalCloneDir = '',
    this.globalAutoPromoteTriage,
    this.globalAutoPromoteRefinement,
    this.globalGeneratePRDescription = false,
    this.localDirBase = const [],
    this.localDirsDetected = const {},
  });

  /// Computed list of monitored repos — this is what the daemon uses.
  /// A repo is monitored if any of its 3 features (PR, IT, Dev) is active.
  List<String> get repositories =>
      (repoConfigs.entries
          .where((e) => e.value.isMonitored)
          .map((e) => e.key)
          .toList()
        ..sort());

  List<String> get knownOrganizations {
    final orgs = <String>{...orgConfigs.keys, ...issueTracking.organizations};
    for (final repo in repoConfigs.keys) {
      final slash = repo.indexOf('/');
      if (slash > 0) orgs.add(repo.substring(0, slash));
    }
    for (final org in orgConfigs.values) {
      final issueOrgs = org.issueOrganizations;
      if (issueOrgs != null) orgs.addAll(issueOrgs);
    }
    for (final repo in repoConfigs.values) {
      final issueOrgs = repo.issueOrganizations;
      if (issueOrgs != null) orgs.addAll(issueOrgs);
    }
    return orgs.where((o) => o.trim().isNotEmpty).toList()..sort();
  }

  List<String> get knownGitHubUsers {
    final users = <String>{
      ...globalPRReviewers,
      ...issueTracking.assignees,
      if (globalPRAssignee.trim().isNotEmpty) globalPRAssignee,
    };
    for (final org in orgConfigs.values) {
      users.addAll(org.prReviewers ?? const <String>[]);
      users.addAll(org.issueAssignees ?? const <String>[]);
      final assignee = org.prAssignee;
      if (assignee != null && assignee.trim().isNotEmpty) users.add(assignee);
    }
    for (final repo in repoConfigs.values) {
      users.addAll(repo.prReviewers ?? const <String>[]);
      users.addAll(repo.issueAssignees ?? const <String>[]);
      final assignee = repo.prAssignee;
      if (assignee != null && assignee.trim().isNotEmpty) users.add(assignee);
    }
    return users.where((u) => u.trim().isNotEmpty).toList()..sort();
  }

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
    IssueTrackingConfig? issueTracking,
    List<String>? globalPRReviewers,
    List<String>? globalPRLabels,
    String? globalPRAssignee,
    bool? globalPRDraft,
    String? globalIssuePrompt,
    String? globalImplementPrompt,
    String? globalTriageOwner,
    String? globalCloneDir,
    Object? globalAutoPromoteTriage = _sentinel,
    Object? globalAutoPromoteRefinement = _sentinel,
    bool? globalGeneratePRDescription,
    List<String>? localDirBase,
    Map<String, String>? localDirsDetected,
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
      issueTracking: issueTracking ?? this.issueTracking,
      globalPRReviewers: globalPRReviewers ?? this.globalPRReviewers,
      globalPRLabels: globalPRLabels ?? this.globalPRLabels,
      globalPRAssignee: globalPRAssignee ?? this.globalPRAssignee,
      globalPRDraft: globalPRDraft ?? this.globalPRDraft,
      globalIssuePrompt: globalIssuePrompt ?? this.globalIssuePrompt,
      globalImplementPrompt:
          globalImplementPrompt ?? this.globalImplementPrompt,
      globalTriageOwner: globalTriageOwner ?? this.globalTriageOwner,
      globalCloneDir: globalCloneDir ?? this.globalCloneDir,
      globalAutoPromoteTriage: globalAutoPromoteTriage == _sentinel
          ? this.globalAutoPromoteTriage
          : globalAutoPromoteTriage as bool?,
      globalAutoPromoteRefinement: globalAutoPromoteRefinement == _sentinel
          ? this.globalAutoPromoteRefinement
          : globalAutoPromoteRefinement as bool?,
      globalGeneratePRDescription:
          globalGeneratePRDescription ?? this.globalGeneratePRDescription,
      localDirBase: localDirBase ?? this.localDirBase,
      localDirsDetected: localDirsDetected ?? this.localDirsDetected,
    );
  }

  Map<String, dynamic> toJson() => {
    if (bindAddr != null) 'bind_addr': bindAddr,
    'server_port': serverPort,
    'poll_interval': pollInterval,
    'repositories': repositories,
    'ai_primary': aiPrimary,
    'ai_fallback': aiFallback,
    'review_mode': reviewMode,
    'retention_days': retentionDays,
    'issue_tracking': issueTracking.toJson(),
    'triage_owner': globalTriageOwner,
    'clone_dir': globalCloneDir,
    'auto_promote_triage': globalAutoPromoteTriage,
    'auto_promote_refinement': globalAutoPromoteRefinement,
    'generate_pr_description': globalGeneratePRDescription,
  };

  factory AppConfig.fromJson(Map<String, dynamic> json) {
    final repos =
        (json['repositories'] as List<dynamic>?)?.cast<String>() ?? [];
    final configs = <String, RepoConfig>{
      // Repos in the monitored list have PR review enabled
      for (final r in repos) r: const RepoConfig(prEnabled: true),
    };
    // Restore non-monitored repos
    final nonMonitored =
        (json['non_monitored'] as List<dynamic>?)?.cast<String>() ?? [];
    for (final r in nonMonitored) {
      configs.putIfAbsent(r, () => const RepoConfig());
    }
    // Per-repo overrides (normalize empty strings to null)
    final overrides = json['repo_overrides'] as Map<String, dynamic>?;
    if (overrides != null) {
      for (final entry in overrides.entries) {
        final ov = entry.value as Map<String, dynamic>;
        final existing = configs[entry.key];
        final itRaw = ov['issue_tracking'] as Map<String, dynamic>?;
        // Derive enabled flags from reality: explicit enabled OR labels configured
        final hasReviewLabels =
            _nullableStringList(itRaw?['review_only_labels']) != null;
        final hasDevLabels =
            _nullableStringList(itRaw?['develop_labels']) != null;
        final itExplicit = itRaw != null && itRaw.containsKey('enabled')
            ? itRaw['enabled'] as bool?
            : null;
        final devExplicit =
            itRaw != null && itRaw.containsKey('develop_enabled')
            ? itRaw['develop_enabled'] as bool?
            : null;
        final fsRaw = ov['first_seen_at'];
        final firstSeen = fsRaw is int
            ? DateTime.fromMillisecondsSinceEpoch(fsRaw * 1000)
            : null;
        configs[entry.key] = RepoConfig(
          prEnabled: existing?.prEnabled,
          itEnabled: itExplicit ?? (hasReviewLabels ? true : null),
          devEnabled: devExplicit ?? (hasDevLabels ? true : null),
          localDir: _nonEmpty(ov['local_dir']),
          triageOwner: _nonEmpty(ov['triage_owner']),
          cloneDir: _nonEmpty(ov['clone_dir']),
          autoPromoteTriage: ov['auto_promote_triage'] as bool?,
          autoPromoteRefinement: ov['auto_promote_refinement'] as bool?,
          generatePRDescription: ov['generate_pr_description'] as bool?,
          aiPrimary: _nonEmpty(ov['primary']),
          aiFallback: _nonEmpty(ov['fallback']),
          reviewMode: _nonEmpty(ov['review_mode']),
          promptId: _nonEmpty(ov['prompt']),
          reviewOnlyLabels: itRaw != null
              ? _nullableStringListAllowEmpty(itRaw['review_only_labels'])
              : null,
          skipLabels: itRaw != null
              ? _nullableStringListAllowEmpty(itRaw['skip_labels'])
              : null,
          issueFilterMode: itRaw != null
              ? _nonEmpty(itRaw['filter_mode'])
              : null,
          issueDefaultAction: itRaw != null
              ? _nonEmpty(itRaw['default_action'])
              : null,
          issueOrganizations: itRaw != null
              ? _nullableStringListAllowEmpty(itRaw['organizations'])
              : null,
          issueAssignees: itRaw != null
              ? _nullableStringListAllowEmpty(itRaw['assignees'])
              : null,
          issuePromptId:
              _nonEmpty(ov['issue_prompt']) ??
              (itRaw != null ? _nonEmpty(itRaw['issue_prompt']) : null),
          developLabels: itRaw != null
              ? _nullableStringListAllowEmpty(itRaw['develop_labels'])
              : null,
          developPromptId:
              _nonEmpty(ov['implement_prompt']) ??
              (itRaw != null ? _nonEmpty(itRaw['develop_prompt']) : null),
          prReviewers: _nullableStringListAllowEmpty(ov['pr_reviewers']),
          prAssignee: _nonEmpty(ov['pr_assignee']),
          prLabels: _nullableStringListAllowEmpty(ov['pr_labels']),
          prDraft: ov['pr_draft'] as bool?,
          firstSeenAt: firstSeen,
        );
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
    final itRaw = json['issue_tracking'] as Map<String, dynamic>?;
    final issueTracking = itRaw != null
        ? IssueTrackingConfig.fromJson(itRaw)
        : const IssueTrackingConfig();

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
      repoConfigs: configs,
      orgConfigs: orgConfigs,
      issueTracking: issueTracking,
      globalPRReviewers: _parseStringList(
        (json['pr_metadata'] as Map<String, dynamic>?)?['reviewers'],
      ),
      globalPRLabels: _parseStringList(
        (json['pr_metadata'] as Map<String, dynamic>?)?['labels'],
      ),
      globalPRAssignee:
          (json['pr_metadata'] as Map<String, dynamic>?)?['pr_assignee']
              as String? ??
          '',
      globalPRDraft:
          (json['pr_metadata'] as Map<String, dynamic>?)?['pr_draft']
              as bool? ??
          false,
      globalIssuePrompt: (json['issue_prompt'] as String?) ?? '',
      globalImplementPrompt: (json['implement_prompt'] as String?) ?? '',
      globalTriageOwner: (json['triage_owner'] as String?) ?? '',
      globalCloneDir: (json['clone_dir'] as String?) ?? '',
      globalAutoPromoteTriage: json['auto_promote_triage'] as bool?,
      globalAutoPromoteRefinement: json['auto_promote_refinement'] as bool?,
      globalGeneratePRDescription:
          (json['generate_pr_description'] as bool?) ?? false,
      localDirBase: _parseStringList(json['local_dir_base']),
      localDirsDetected: localDirsDetected,
    );
  }

  static List<String> _parseStringList(dynamic v) {
    if (v is List) return v.cast<String>();
    return const [];
  }
}
