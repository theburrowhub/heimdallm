class ReviewPrompt {
  final String id;
  final String name;
  final String focus;        // 'general' | 'security' | 'performance' | 'architecture' | 'docs' | 'custom'
  final String instructions; // plain-text focus instructions (simple mode)
  final String prompt;       // full template with {placeholders} (advanced mode, overrides instructions)
  final String cliFlags;     // extra CLI flags (e.g. --model claude-opus-4-6)
  // Whether this prompt is the active review profile. The daemon keeps at
  // most one agent active at a time.
  final bool isDefaultPr;

  const ReviewPrompt({
    required this.id,
    required this.name,
    this.focus = 'general',
    this.instructions = '',
    this.prompt = '',
    this.cliFlags = '',
    this.isDefaultPr = false,
  });

  ReviewPrompt copyWith({
    String? id, String? name, String? focus, String? instructions,
    String? prompt, String? cliFlags, bool? isDefaultPr,
  }) => ReviewPrompt(
    id: id ?? this.id,
    name: name ?? this.name,
    focus: focus ?? this.focus,
    instructions: instructions ?? this.instructions,
    prompt: prompt ?? this.prompt,
    cliFlags: cliFlags ?? this.cliFlags,
    isDefaultPr: isDefaultPr ?? this.isDefaultPr,
  );

  factory ReviewPrompt.fromJson(Map<String, dynamic> json) => ReviewPrompt(
    id: json['id'] as String,
    name: json['name'] as String,
    focus: (json['focus'] as String?) ?? 'general',
    instructions: (json['instructions'] as String?) ?? '',
    prompt: (json['prompt'] as String?) ?? '',
    cliFlags: (json['cli_flags'] as String?) ?? '',
    // Accept both the per-category key and the legacy `is_default` flag.
    // The explicit key always wins, so `is_default: true` +
    // `is_default_pr: false` yields isDefaultPr=false, not a fallback to true.
    isDefaultPr: _parseBool(json['is_default_pr']) ??
        _parseBool(json['is_default']) ??
        false,
  );

  Map<String, dynamic> toJson() => {
    'id': id,
    'name': name,
    'cli': 'claude', // kept for daemon compatibility
    'focus': focus,
    'instructions': instructions,
    'prompt': prompt,
    'cli_flags': cliFlags,
    'is_default_pr': isDefaultPr,
  };

  bool get hasPRReview => prompt.isNotEmpty || instructions.isNotEmpty;

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
}

class PresetDef {
  final String id, name, focus;
  final String instructions;
  const PresetDef({
    required this.id,
    required this.name,
    required this.focus,
    this.instructions = '',
  });
}

// Backwards compat alias
typedef Agent = ReviewPrompt;

bool? _parseBool(dynamic v) {
  if (v is bool) return v;
  if (v is num) return v != 0;
  if (v is String) {
    if (v == 'true' || v == '1') return true;
    if (v == 'false' || v == '0' || v.isEmpty) return false;
  }
  return null;
}
