import 'dart:convert';

/// The kind of control a schema field renders as. Mirrors the daemon's
/// agentschema.FieldType.
enum AgentFieldType {
  select,
  bool,
  number,
  text,

  /// A type this app does not know. The daemon never serves one; a schema
  /// typed by hand can contain one, and validation reports it.
  unknown;

  static AgentFieldType parse(String? raw) => switch (raw) {
    'select' => AgentFieldType.select,
    'bool' => AgentFieldType.bool,
    'number' => AgentFieldType.number,
    'text' => AgentFieldType.text,
    _ => AgentFieldType.unknown,
  };
}

/// One configurable option of an agent (GET /agent-schemas).
class AgentSchemaField {
  /// Where the value is stored under `[ai.agents.<name>]`.
  final String key;
  final AgentFieldType type;
  final String label;
  final String description;

  /// The CLI option the value is passed with.
  final String flag;

  /// For a bool on by default: the flag passed when it is off.
  final String falseFlag;
  final List<String> options;

  /// A select whose options are suggestions: any value is accepted.
  final bool allowCustom;

  /// The value used when the operator has not set one (String, bool or num).
  final Object? defaultValue;

  /// Names the empty choice of a select ("CLI default").
  final String emptyLabel;
  final num? min;
  final num? max;

  /// A dangerous toggle: the app may only turn it off.
  final bool warning;

  const AgentSchemaField({
    required this.key,
    required this.type,
    required this.label,
    this.description = '',
    this.flag = '',
    this.falseFlag = '',
    this.options = const [],
    this.allowCustom = false,
    this.defaultValue,
    this.emptyLabel = '',
    this.min,
    this.max,
    this.warning = false,
  });

  factory AgentSchemaField.fromJson(Map<String, dynamic> json) =>
      AgentSchemaField(
        key: (json['key'] as String?) ?? '',
        type: AgentFieldType.parse(json['type'] as String?),
        label: (json['label'] as String?) ?? '',
        description: (json['description'] as String?) ?? '',
        flag: (json['flag'] as String?) ?? '',
        falseFlag: (json['false_flag'] as String?) ?? '',
        options: [for (final o in (json['options'] as List?) ?? const []) '$o'],
        allowCustom: (json['allow_custom'] as bool?) ?? false,
        defaultValue: json['default'],
        emptyLabel: (json['empty_label'] as String?) ?? '',
        min: json['min'] as num?,
        max: json['max'] as num?,
        warning: (json['warning'] as bool?) ?? false,
      );

  Map<String, dynamic> toJson() => {
    'key': key,
    'type': type.name,
    'label': label,
    if (description.isNotEmpty) 'description': description,
    if (flag.isNotEmpty) 'flag': flag,
    if (falseFlag.isNotEmpty) 'false_flag': falseFlag,
    if (options.isNotEmpty) 'options': options,
    if (allowCustom) 'allow_custom': true,
    if (defaultValue != null) 'default': defaultValue,
    if (emptyLabel.isNotEmpty) 'empty_label': emptyLabel,
    if (min != null) 'min': min,
    if (max != null) 'max': max,
    if (warning) 'warning': true,
  };

  AgentSchemaField copyWith({
    String? key,
    AgentFieldType? type,
    String? label,
    String? description,
    String? flag,
    String? falseFlag,
    List<String>? options,
    bool? allowCustom,
    Object? defaultValue = _keep,
    String? emptyLabel,
    Object? min = _keep,
    Object? max = _keep,
    bool? warning,
  }) => AgentSchemaField(
    key: key ?? this.key,
    type: type ?? this.type,
    label: label ?? this.label,
    description: description ?? this.description,
    flag: flag ?? this.flag,
    falseFlag: falseFlag ?? this.falseFlag,
    options: options ?? this.options,
    allowCustom: allowCustom ?? this.allowCustom,
    defaultValue: identical(defaultValue, _keep)
        ? this.defaultValue
        : defaultValue,
    emptyLabel: emptyLabel ?? this.emptyLabel,
    min: identical(min, _keep) ? this.min : min as num?,
    max: identical(max, _keep) ? this.max : max as num?,
    warning: warning ?? this.warning,
  );

  /// The value the field takes for [values]: the operator's value when set,
  /// the default otherwise.
  Object? effective(Map<String, dynamic> values) => values[key] ?? defaultValue;
}

const _keep = Object();

/// An agent's schema: its identity and the fields of its settings form.
class AgentSchema {
  final String name;
  final String displayName;
  final String description;
  final String iconColor;

  /// The executable a custom agent runs ('' for built-in agents).
  final String binary;
  final List<AgentSchemaField> fields;

  /// True for the agents Heimdallm ships.
  final bool builtin;

  const AgentSchema({
    required this.name,
    this.displayName = '',
    this.description = '',
    this.iconColor = '',
    this.binary = '',
    this.fields = const [],
    this.builtin = false,
  });

  factory AgentSchema.fromJson(Map<String, dynamic> json) => AgentSchema(
    name: (json['name'] as String?) ?? '',
    displayName: (json['display_name'] as String?) ?? '',
    description: (json['description'] as String?) ?? '',
    iconColor: (json['icon_color'] as String?) ?? '',
    binary: (json['binary'] as String?) ?? '',
    fields: [
      for (final f in (json['fields'] as List?) ?? const [])
        AgentSchemaField.fromJson(f as Map<String, dynamic>),
    ],
    builtin: (json['builtin'] as bool?) ?? false,
  );

  /// The schema as the daemon accepts it (PUT /agent-schemas/{name}).
  Map<String, dynamic> toJson() => {
    'name': name,
    if (displayName.isNotEmpty) 'display_name': displayName,
    if (description.isNotEmpty) 'description': description,
    if (iconColor.isNotEmpty) 'icon_color': iconColor,
    if (binary.isNotEmpty) 'binary': binary,
    'fields': [for (final f in fields) f.toJson()],
  };

  String get title => displayName.isNotEmpty ? displayName : name;

  AgentSchemaField? field(String key) {
    for (final f in fields) {
      if (f.key == key) return f;
    }
    return null;
  }

  /// Pretty JSON, for the editor and for copying into a schema file.
  String toPrettyJson() => const JsonEncoder.withIndent('  ').convert(toJson());

  /// The config.toml section that declares this agent with its schema inline,
  /// for operators who define custom agents by hand.
  String toTomlSnippet() {
    final buf = StringBuffer()
      ..writeln('[ai.agents.$name]')
      ..writeln('binary = ${_tomlString(binary)}')
      ..writeln("schema = '''")
      // A quote in a label must not close the ''' string: as JSON, \u0027
      // reads back as the same quote.
      ..writeln(
        jsonEncode({...toJson()}..remove('binary')).replaceAll("'", r'\u0027'),
      )
      ..writeln("'''");
    return buf.toString();
  }

  AgentSchema copyWith({
    String? name,
    String? displayName,
    String? description,
    String? iconColor,
    String? binary,
    List<AgentSchemaField>? fields,
  }) => AgentSchema(
    name: name ?? this.name,
    displayName: displayName ?? this.displayName,
    description: description ?? this.description,
    iconColor: iconColor ?? this.iconColor,
    binary: binary ?? this.binary,
    fields: fields ?? this.fields,
    builtin: builtin,
  );
}

String _tomlString(String s) => jsonEncode(s);

/// GET /agent-schemas.
class AgentSchemaCatalog {
  final List<AgentSchema> schemas;

  /// Whether config.toml opts in to managing custom agents from the app
  /// (`[ai] allow_custom_agents_api = true`).
  final bool customAgentsApiEnabled;

  const AgentSchemaCatalog({
    this.schemas = const [],
    this.customAgentsApiEnabled = false,
  });

  factory AgentSchemaCatalog.fromJson(Map<String, dynamic> json) =>
      AgentSchemaCatalog(
        schemas: [
          for (final s in (json['schemas'] as List?) ?? const [])
            AgentSchema.fromJson(s as Map<String, dynamic>),
        ],
        customAgentsApiEnabled:
            (json['custom_agents_api_enabled'] as bool?) ?? false,
      );

  AgentSchema? byName(String name) {
    for (final s in schemas) {
      if (s.name == name) return s;
    }
    return null;
  }

  List<AgentSchema> get custom => [
    for (final s in schemas)
      if (!s.builtin) s,
  ];
}

/// Client-side checks that mirror the daemon's schema rules, so the editor
/// can point at a problem before saving. The daemon validates again.
final agentNamePattern = RegExp(r'^[a-z][a-z0-9_-]{0,31}$');
final fieldKeyPattern = RegExp(r'^[a-z][a-z0-9_]{0,63}$');
final flagPattern = RegExp(r'^--?[A-Za-z0-9][A-Za-z0-9-]*$');
final binaryPattern = RegExp(r'^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$');

/// Keys a schema field may not use (shared settings of every agent).
const reservedFieldKeys = {
  'binary',
  'schema',
  'schema_path',
  'options',
  'prompt',
  'extra_flags',
  'execution_timeout',
  'review_limits',
};

// Limits and rules of the daemon's agentschema package. They are checked
// against the same cases in both test suites
// (daemon/internal/agentschema/testdata/custom_schema_cases.json).
const maxSchemaFields = 64;
const maxSchemaOptions = 200;
const maxSchemaLabelLength = 128;
const maxSchemaDescriptionLength = 1000;
const maxSchemaOptionLength = 128;
const maxSchemaTextValueLength = 512;
final iconColorPattern = RegExp(r'^#[0-9A-Fa-f]{6}$');

/// Shells, interpreters and launchers a custom agent may not run: they would
/// execute the review prompt they read on stdin. Mirrors the daemon's list;
/// entries are lowercase because the check lowercases.
const deniedAgentBinaries = {
  'sh', 'bash', 'zsh', 'fish', 'dash', 'ksh', 'mksh', 'csh', 'tcsh', 'ash', //
  'busybox', 'nu', 'elvish', 'xonsh', 'pwsh', 'powershell', 'cmd', 'env', //
  'sudo', 'doas', 'su', 'xargs', 'nohup', 'timeout', 'nice', 'exec', 'eval', //
  'command', 'script', 'expect', 'watch', 'time', 'stdbuf', 'node', 'nodejs', //
  'deno',
  'bun',
  'npx',
  'bunx',
  'ruby',
  'irb',
  'perl',
  'php',
  'lua',
  'luajit', //
  'tclsh', 'wish', 'osascript', 'awk', 'gawk', 'mawk', 'nawk', 'sed', //
  'jshell', 'java', 'groovy', 'scala', 'r', 'rscript', 'julia', 'swift', //
  'dotnet', 'go', 'make', 'gdb', 'lldb', 'vim', 'vi', 'nvim', 'emacs', 'ed', //
  'ex', 'docker', 'podman', 'kubectl', 'ssh', 'nc', 'ncat', 'socat', 'curl', //
  'wget', 'git', 'find', 'heimdallm', 'ipython', 'bpython',
};

/// Versioned or variant names of the denied programs: python3, python3.12,
/// ksh93, tclsh8.6, ipython3, perl5.36… Mirrors the daemon's list.
const _interpreterFamilies = [
  'python', 'pypy', 'ipython', 'bpython', 'perl', 'ruby', 'irb', 'php', //
  'node', 'lua', 'luajit', 'deno', 'bun', 'julia', 'rscript', 'java', //
  'tclsh', 'wish', 'awk', 'gawk', 'sh', 'ash', 'dash', 'bash', 'zsh', //
  'ksh', 'mksh', 'csh', 'tcsh', 'fish', 'pwsh', 'powershell', //
];

/// Whether [binary] is a shell or an interpreter. It compares
/// case-insensitively and without a Windows executable suffix: on a
/// case-insensitive filesystem RSCRIPT runs Rscript, and bash.exe is bash.
bool isDeniedAgentBinary(String binary) {
  var name = binary.toLowerCase();
  for (final ext in const ['.exe', '.com', '.cmd', '.bat']) {
    if (name.endsWith(ext)) {
      name = name.substring(0, name.length - ext.length);
      break;
    }
  }
  if (deniedAgentBinaries.contains(name)) return true;
  for (final prefix in _interpreterFamilies) {
    if (name.startsWith(prefix)) {
      final rest = name.substring(prefix.length);
      if (rest.isEmpty || RegExp(r'^[0-9.\-]+$').hasMatch(rest)) return true;
    }
  }
  return false;
}

bool _hasControl(String s, {bool allowNewline = false}) => s.runes.any(
  (r) => (r < 0x20 || (r >= 0x7f && r < 0xa0)) && !(allowNewline && r == 0x0a),
);

/// Checks free text (labels, descriptions); null when it is fine.
String? _textProblem(String what, String value, int max) {
  if (value.runes.length > max) return '$what is longer than $max characters.';
  if (_hasControl(value, allowNewline: true)) {
    return '$what contains a control character.';
  }
  return null;
}

/// Checks a value that reaches the CLI as an argument; null when it is fine.
String? _argProblem(String value, int max) {
  if (value.runes.length > max) return 'is longer than $max characters';
  if (value.trim().startsWith('-')) return 'must not begin with "-"';
  if (_hasControl(value)) return 'contains a control character';
  return null;
}

bool _isDangerousFlag(String flag) {
  final lower = flag.toLowerCase();
  return lower.startsWith('--danger') ||
      lower.startsWith('--allow-dangerously');
}

/// Why [value] is not a valid value of [f], or null when it is. Mirrors the
/// daemon's normalizeValue.
String? fieldValueProblem(AgentSchemaField f, Object? value) {
  switch (f.type) {
    case AgentFieldType.bool:
      return value is bool ? null : 'must be on or off';
    case AgentFieldType.number:
      if (value is! num || value.isNaN || value.isInfinite) {
        return 'must be a number';
      }
      if (value < 0) return 'must not be negative';
      if (f.min != null && value < f.min!) {
        return 'is below the minimum ${f.min}';
      }
      if (f.max != null && value > f.max!) {
        return 'is above the maximum ${f.max}';
      }
      return null;
    case AgentFieldType.select:
    case AgentFieldType.text:
      if (value is! String) return 'must be text';
      final v = value.trim();
      final arg = _argProblem(
        v,
        f.type == AgentFieldType.select
            ? maxSchemaOptionLength
            : maxSchemaTextValueLength,
      );
      if (arg != null) return arg;
      if (v.toLowerCase() == 'bypasspermissions') {
        return 'is never passed to an agent';
      }
      if (f.type == AgentFieldType.select &&
          v.isNotEmpty &&
          !f.allowCustom &&
          !f.options.contains(v)) {
        return 'is not one of the options';
      }
      return null;
    case AgentFieldType.unknown:
      return 'has an unknown type';
  }
}

/// Problems with a custom agent's schema, empty when it looks valid. It
/// applies the daemon's rules (agentschema Validate, ValidateCustom and
/// ValidateBinary), so a schema the editor accepts — saved through the API
/// or pasted into config.toml — is one the daemon loads.
/// [builtinNames] are names a custom agent may not take.
List<String> validateCustomSchema(
  AgentSchema s, {
  Set<String> builtinNames = const {},
}) {
  final errors = <String>[];
  if (!agentNamePattern.hasMatch(s.name)) {
    errors.add(
      'Name: lowercase letters, digits, "-" or "_", starting with a letter.',
    );
  } else if (builtinNames.contains(s.name)) {
    errors.add('Name: "${s.name}" is a built-in agent.');
  }
  if (!binaryPattern.hasMatch(s.binary)) {
    errors.add('Binary: an executable name on PATH, without a path.');
  } else if (isDeniedAgentBinary(s.binary)) {
    errors.add(
      'Binary: "${s.binary}" is a shell or an interpreter; it would run the '
      'review prompt it reads.',
    );
  }
  for (final p in [
    _textProblem('Display name', s.displayName, maxSchemaLabelLength),
    _textProblem('Description', s.description, maxSchemaDescriptionLength),
  ]) {
    if (p != null) errors.add(p);
  }
  if (s.iconColor.isNotEmpty && !iconColorPattern.hasMatch(s.iconColor)) {
    errors.add('Icon colour: a #RRGGBB colour.');
  }
  if (s.fields.length > maxSchemaFields) {
    errors.add('At most $maxSchemaFields options.');
  }
  final keys = <String>{};
  final flags = <String>{};
  for (final (i, f) in s.fields.indexed) {
    final where = 'Option ${i + 1}${f.key.isEmpty ? '' : ' (${f.key})'}';
    void add(String problem) => errors.add('$where: $problem');
    if (!fieldKeyPattern.hasMatch(f.key)) {
      add('key must be lowercase letters, digits or "_".');
    } else if (reservedFieldKeys.contains(f.key)) {
      add('"${f.key}" is reserved.');
    } else if (!keys.add(f.key)) {
      add('key is used twice.');
    }
    if (f.label.trim().isEmpty) add('label is required.');
    for (final p in [
      _textProblem('label', f.label, maxSchemaLabelLength),
      _textProblem('help text', f.description, maxSchemaDescriptionLength),
      _textProblem('empty choice', f.emptyLabel, maxSchemaLabelLength),
    ]) {
      if (p != null) add(p);
    }
    if (f.flag.isEmpty) {
      add('a flag is required: options reach a custom agent only as flags.');
    } else if (!flagPattern.hasMatch(f.flag)) {
      add('flag must look like --name or -n.');
    } else if (!flags.add(f.flag)) {
      add('flag ${f.flag} is used twice.');
    }
    if (f.falseFlag.isNotEmpty) {
      if (f.type != AgentFieldType.bool) {
        add('a "flag when off" only applies to toggles.');
      } else if (!flagPattern.hasMatch(f.falseFlag)) {
        add('flag when off must look like --name or -n.');
      } else if (!flags.add(f.falseFlag)) {
        add('flag ${f.falseFlag} is used twice.');
      }
    }
    if ((_isDangerousFlag(f.flag) || _isDangerousFlag(f.falseFlag)) &&
        (f.type != AgentFieldType.bool || !f.warning)) {
      add(
        'a --danger…/--allow-dangerously… flag must be a toggle marked dangerous.',
      );
    }
    switch (f.type) {
      case AgentFieldType.select:
        if (f.options.isEmpty && !f.allowCustom) {
          add('a dropdown needs options (or allow any value).');
        }
        if (f.options.length > maxSchemaOptions) {
          add('at most $maxSchemaOptions options.');
        }
        final seen = <String>{};
        for (final o in f.options) {
          final p = _argProblem(o, maxSchemaOptionLength);
          if (p != null) {
            add('option "$o" $p.');
          } else if (o.isEmpty) {
            add('options must not be empty; the empty choice is implicit.');
          } else if (!seen.add(o)) {
            add('option "$o" is listed twice.');
          }
        }
      case AgentFieldType.number:
        if ((f.min ?? 0) < 0 || (f.max ?? 0) < 0) {
          add('min and max must not be negative.');
        } else if (f.min != null && f.max != null && f.min! > f.max!) {
          add('min is above max.');
        }
      case AgentFieldType.bool:
      case AgentFieldType.text:
        break;
      case AgentFieldType.unknown:
        add('type must be a dropdown, toggle, number or text.');
    }
    if (f.type != AgentFieldType.select &&
        (f.options.isNotEmpty || f.allowCustom)) {
      add('options only apply to dropdowns.');
    }
    if (f.type != AgentFieldType.number && (f.min != null || f.max != null)) {
      add('min and max only apply to numbers.');
    }
    if (f.warning && f.type != AgentFieldType.bool) {
      add('only a toggle can be marked dangerous.');
    }
    if (f.key == 'model' &&
        f.type != AgentFieldType.select &&
        f.type != AgentFieldType.text) {
      add('model must be a dropdown or text.');
    }
    if (f.defaultValue != null && f.type != AgentFieldType.unknown) {
      final p = fieldValueProblem(f, f.defaultValue);
      if (p != null) {
        add('the default $p.');
      } else if (f.warning && f.defaultValue == true) {
        add('a dangerous toggle must default to off.');
      }
    }
  }
  return errors;
}
