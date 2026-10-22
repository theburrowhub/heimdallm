import 'dart:convert';

/// The kind of control a schema field renders as. Mirrors the daemon's
/// agentschema.FieldType.
enum AgentFieldType {
  select,
  bool,
  number,
  text;

  static AgentFieldType parse(String? raw) => switch (raw) {
    'select' => AgentFieldType.select,
    'bool' => AgentFieldType.bool,
    'number' => AgentFieldType.number,
    _ => AgentFieldType.text,
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

/// Problems with a custom agent's schema, empty when it looks valid.
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
  }
  final keys = <String>{};
  final flags = <String>{};
  for (final (i, f) in s.fields.indexed) {
    final where = 'Field ${i + 1}${f.key.isEmpty ? '' : ' (${f.key})'}';
    if (!fieldKeyPattern.hasMatch(f.key)) {
      errors.add('$where: key must be lowercase letters, digits or "_".');
    } else if (reservedFieldKeys.contains(f.key)) {
      errors.add('$where: "${f.key}" is reserved.');
    } else if (!keys.add(f.key)) {
      errors.add('$where: key is used twice.');
    }
    if (f.label.trim().isEmpty) errors.add('$where: label is required.');
    if (!flagPattern.hasMatch(f.flag)) {
      errors.add('$where: flag must look like --name or -n.');
    } else if (!flags.add(f.flag)) {
      errors.add('$where: flag ${f.flag} is used twice.');
    }
    if (f.falseFlag.isNotEmpty) {
      if (f.type != AgentFieldType.bool) {
        errors.add('$where: a "flag when off" only applies to toggles.');
      } else if (!flagPattern.hasMatch(f.falseFlag)) {
        errors.add('$where: flag when off must look like --name or -n.');
      } else if (!flags.add(f.falseFlag)) {
        errors.add('$where: flag ${f.falseFlag} is used twice.');
      }
    }
    if (f.type == AgentFieldType.select &&
        f.options.isEmpty &&
        !f.allowCustom) {
      errors.add('$where: a dropdown needs options (or allow any value).');
    }
    if (f.options.any((o) => o.trim().startsWith('-'))) {
      errors.add('$where: options must not begin with "-".');
    }
    if (f.type == AgentFieldType.number) {
      if ((f.min ?? 0) < 0 || (f.max ?? 0) < 0) {
        errors.add('$where: min and max must not be negative.');
      } else if (f.min != null && f.max != null && f.min! > f.max!) {
        errors.add('$where: min is above max.');
      }
    }
    if (f.warning && f.defaultValue == true) {
      errors.add('$where: a dangerous toggle must default to off.');
    }
    if (f.key == 'model' &&
        f.type != AgentFieldType.select &&
        f.type != AgentFieldType.text) {
      errors.add('$where: model must be a dropdown or text.');
    }
  }
  return errors;
}
