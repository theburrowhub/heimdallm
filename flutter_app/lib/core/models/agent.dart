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

  static ReviewPrompt fromPreset(PresetDef p) => ReviewPrompt(
    id: p.id,
    name: p.name,
    focus: p.focus,
    instructions: p.instructions,
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
  final String id, name, focus, instructions;
  const PresetDef({required this.id, required this.name, required this.focus, required this.instructions});
}

// Backwards compat alias
typedef Agent = ReviewPrompt;
