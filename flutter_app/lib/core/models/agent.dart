class ReviewPrompt {
  final String id;
  final String name;
  final String focus;        // 'general' | 'security' | 'performance' | 'architecture' | 'docs' | 'custom'
  final String instructions; // plain-text focus instructions (simple mode)
  final String prompt;       // full template with {placeholders} (advanced mode, overrides instructions)
  final String cliFlags;     // extra CLI flags (e.g. --model claude-opus-4-6)
  final bool isDefault;
  // Issue triage prompts
  final String issuePrompt;
  final String issueInstructions;
  // Auto-implement prompts
  final String implementPrompt;
  final String implementInstructions;

  const ReviewPrompt({
    required this.id,
    required this.name,
    this.focus = 'general',
    this.instructions = '',
    this.prompt = '',
    this.cliFlags = '',
    this.isDefault = false,
    this.issuePrompt = '',
    this.issueInstructions = '',
    this.implementPrompt = '',
    this.implementInstructions = '',
  });

  ReviewPrompt copyWith({
    String? id, String? name, String? focus, String? instructions,
    String? prompt, String? cliFlags, bool? isDefault,
    String? issuePrompt, String? issueInstructions,
    String? implementPrompt, String? implementInstructions,
  }) => ReviewPrompt(
    id: id ?? this.id,
    name: name ?? this.name,
    focus: focus ?? this.focus,
    instructions: instructions ?? this.instructions,
    prompt: prompt ?? this.prompt,
    cliFlags: cliFlags ?? this.cliFlags,
    isDefault: isDefault ?? this.isDefault,
    issuePrompt: issuePrompt ?? this.issuePrompt,
    issueInstructions: issueInstructions ?? this.issueInstructions,
    implementPrompt: implementPrompt ?? this.implementPrompt,
    implementInstructions: implementInstructions ?? this.implementInstructions,
  );

  factory ReviewPrompt.fromJson(Map<String, dynamic> json) => ReviewPrompt(
    id: json['id'] as String,
    name: json['name'] as String,
    focus: (json['focus'] as String?) ?? 'general',
    instructions: (json['instructions'] as String?) ?? '',
    prompt: (json['prompt'] as String?) ?? '',
    cliFlags: (json['cli_flags'] as String?) ?? '',
    isDefault: (json['is_default'] as bool?) ?? false,
    issuePrompt: (json['issue_prompt'] as String?) ?? '',
    issueInstructions: (json['issue_instructions'] as String?) ?? '',
    implementPrompt: (json['implement_prompt'] as String?) ?? '',
    implementInstructions: (json['implement_instructions'] as String?) ?? '',
  );

  Map<String, dynamic> toJson() => {
    'id': id,
    'name': name,
    'cli': 'claude', // kept for daemon compatibility
    'focus': focus,
    'instructions': instructions,
    'prompt': prompt,
    'cli_flags': cliFlags,
    'is_default': isDefault,
    'issue_prompt': issuePrompt,
    'issue_instructions': issueInstructions,
    'implement_prompt': implementPrompt,
    'implement_instructions': implementInstructions,
  };

  // ── Category helpers ──────────────────────────────────────────────────────
  bool get hasPRReview => prompt.isNotEmpty || instructions.isNotEmpty;
  bool get hasIssueTriage => issuePrompt.isNotEmpty || issueInstructions.isNotEmpty;
  bool get hasDevelopment => implementPrompt.isNotEmpty || implementInstructions.isNotEmpty;

  // ── Preset templates ────────────────────────────────────────────────────────

  static const presets = [
    PresetDef(
      id: 'preset-general',
      name: 'General Review',
      focus: 'general',
      instructions: 'Perform a comprehensive code review covering correctness, '
          'maintainability, error handling, edge cases, and code style. '
          'Flag any bugs, anti-patterns, or opportunities for improvement.',
    ),
    PresetDef(
      id: 'preset-security',
      name: 'Security Audit',
      focus: 'security',
      instructions: 'Focus exclusively on security vulnerabilities: injection attacks '
          '(SQL, command, XSS), authentication/authorization flaws, sensitive data exposure, '
          'insecure dependencies, hardcoded secrets, and OWASP Top 10 issues. '
          'Treat every potential attack vector as high severity.',
    ),
    PresetDef(
      id: 'preset-performance',
      name: 'Performance Review',
      focus: 'performance',
      instructions: 'Focus on performance bottlenecks: N+1 queries, missing indexes, '
          'unnecessary allocations, blocking I/O in hot paths, inefficient algorithms (O(n²) or worse), '
          'missing caching opportunities, and memory leaks.',
    ),
    PresetDef(
      id: 'preset-architecture',
      name: 'Architecture Review',
      focus: 'architecture',
      instructions: 'Evaluate architectural concerns: SOLID principles violations, '
          'excessive coupling, missing abstractions, violation of separation of concerns, '
          'incorrect layer dependencies, and scalability issues.',
    ),
    PresetDef(
      id: 'preset-docs',
      name: 'Docs & Style',
      focus: 'docs',
      instructions: 'Review documentation quality and code style: missing or misleading '
          'docstrings, unclear variable/function names, magic numbers without explanation, '
          'inconsistent naming conventions, and missing error messages.',
    ),
  ];

  static const issueTriagePresets = [
    PresetDef(
      id: 'preset-triage-bug',
      name: 'Bug Triage',
      focus: 'general',
      issueInstructions:
          'Treat this as a bug report. Verify whether the body and comments describe '
          'a reproducible failure (steps, expected vs actual, environment). Identify the '
          'affected component, estimate severity (crash / data-loss / functional / cosmetic), '
          'and suggest a priority. Recommend labels (e.g. bug, high-priority, needs-repro) '
          'and flag if a minimal repro is missing.',
    ),
    PresetDef(
      id: 'preset-triage-feature',
      name: 'Feature Request',
      focus: 'architecture',
      issueInstructions:
          'Evaluate this as a feature request. Assess fit with the existing architecture, '
          'sketch one or two alternative approaches, and estimate implementation effort '
          '(S / M / L). Flag unclear scope or missing acceptance criteria. Recommend labels '
          '(e.g. feature, discussion-needed) and note whether a design doc is warranted '
          'before implementation.',
    ),
    PresetDef(
      id: 'preset-triage-duplicate',
      name: 'Duplicate Detection',
      focus: 'general',
      issueInstructions:
          'Check whether this issue duplicates an existing one. Scan titles, bodies, error '
          'messages, and referenced files for overlap with other open or recently closed '
          'issues. If a likely duplicate exists, recommend closing with a link to the '
          'original. Otherwise, flag the issue as unique and ready for normal triage.',
    ),
    PresetDef(
      id: 'preset-triage-security',
      name: 'Security Triage',
      focus: 'security',
      issueInstructions:
          'Treat this as a security report. Identify the attack vector, estimate a '
          'CVSS-style severity, and flag whether sensitive details appear in the public '
          'body (if so, recommend moving to a private disclosure channel). Suggest urgency '
          'labels. Never post exploit details or reproduction steps in public comments.',
    ),
    PresetDef(
      id: 'preset-triage-quick-win',
      name: 'Quick Win',
      focus: 'general',
      issueInstructions:
          'Identify whether this issue is trivially closeable: typos, broken links, '
          'one-line config changes, or obvious docs fixes. If so, estimate the size in LOC '
          'and recommend labels (good-first-issue, quick-win). Otherwise, flag it for '
          'normal triage with a one-line reason.',
    ),
  ];

  static const developmentPresets = [
    PresetDef(
      id: 'preset-dev-plan-first',
      name: 'Plan First',
      focus: 'architecture',
      implementInstructions:
          'Before writing code, produce a short plan in the PR description: files to '
          'touch, approach, and risks. Implement only after the plan is clear. Keep the '
          'diff aligned with the plan — out-of-scope changes belong in a follow-up PR.',
    ),
    PresetDef(
      id: 'preset-dev-tdd',
      name: 'Test-Driven',
      focus: 'general',
      implementInstructions:
          'Follow the TDD cycle. Write a failing test that captures the expected '
          'behaviour, then the minimal implementation to make it pass, then refactor if '
          'something obvious emerges. Every behavioural change must have a test. Do not '
          'commit without a green test run.',
    ),
    PresetDef(
      id: 'preset-dev-minimal',
      name: 'Minimal Patch',
      focus: 'general',
      implementInstructions:
          'Produce the smallest possible diff that resolves the issue. No refactoring of '
          'surrounding code, no style cleanups, no new abstractions. If neighbouring code '
          'has problems, leave it alone — call the smell out in the PR body instead.',
    ),
    PresetDef(
      id: 'preset-dev-refactor-safe',
      name: 'Refactor-Safe',
      focus: 'performance',
      implementInstructions:
          'When the fix requires touching surrounding code, preserve existing behaviour '
          'exactly. If coverage is thin, add characterisation tests first. Keep the '
          'behavioural change and the refactor in separate commits so they can be reviewed '
          'independently.',
    ),
    PresetDef(
      id: 'preset-dev-docs-only',
      name: 'Docs Only',
      focus: 'docs',
      implementInstructions:
          'This is a documentation-only change. Do not modify executable code, configs, '
          'or tests. Update only markdown files, comments, or docstrings. Verify that '
          'markdown renders correctly (no broken links, valid code fences).',
    ),
  ];

  static ReviewPrompt fromPreset(PresetDef p) => ReviewPrompt(
    id: p.id,
    name: p.name,
    focus: p.focus,
    instructions: p.instructions,
    issueInstructions: p.issueInstructions,
    implementInstructions: p.implementInstructions,
  );

  static const placeholders = [
    '{title}', '{number}', '{repo}', '{author}', '{link}', '{diff}',
  ];

  static const issuePlaceholders = [
    '{repo}', '{number}', '{title}', '{author}', '{labels}', '{body}', '{comments}',
  ];

  static const implementPlaceholders = [
    '{repo}', '{number}', '{title}', '{author}', '{labels}', '{body}', '{comments}',
  ];
}

class PresetDef {
  final String id, name, focus;
  // At most one of these three should be set per preset — a preset belongs
  // to exactly one category (PR review, issue triage, or development) and
  // `fromPreset` propagates whichever is non-empty into the corresponding
  // `ReviewPrompt` field. Keeping them on a single class avoids having to
  // juggle three near-identical types and mirrors the data shape the
  // daemon already stores on the agent record.
  final String instructions;
  final String issueInstructions;
  final String implementInstructions;
  const PresetDef({
    required this.id,
    required this.name,
    required this.focus,
    this.instructions = '',
    this.issueInstructions = '',
    this.implementInstructions = '',
  });
}

// Backwards compat alias
typedef Agent = ReviewPrompt;
