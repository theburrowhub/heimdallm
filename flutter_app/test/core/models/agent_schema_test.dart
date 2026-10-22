import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/agent_schema.dart';

import '../../support/agent_schemas.dart';

const _aider = {
  'name': 'aider',
  'display_name': 'Aider',
  'binary': 'aider',
  'icon_color': '#123ABC',
  'description': 'Pair programming',
  'fields': [
    {
      'key': 'model',
      'type': 'select',
      'label': 'Model',
      'flag': '--model',
      'options': ['gpt-4o', 'deepseek-chat'],
      'default': 'gpt-4o',
      'empty_label': 'CLI default',
    },
    {
      'key': 'auto_commits',
      'type': 'bool',
      'label': '--auto-commits',
      'flag': '--auto-commits',
      'false_flag': '--no-auto-commits',
      'default': true,
      'description': 'Commit each edit',
    },
    {
      'key': 'map_tokens',
      'type': 'number',
      'label': '--map-tokens',
      'flag': '--map-tokens',
      'min': 0,
      'max': 8192,
    },
    {
      'key': 'yolo',
      'type': 'bool',
      'label': '--yes-always',
      'flag': '--yes-always',
      'warning': true,
    },
    {'key': 'notes', 'type': 'text', 'label': 'Notes', 'flag': '--notes'},
    {
      'key': 'variant',
      'type': 'select',
      'label': 'Variant',
      'flag': '--variant',
      'options': ['a'],
      'allow_custom': true,
    },
  ],
};

void main() {
  test('round-trips through JSON', () {
    final s = AgentSchema.fromJson(_aider);
    expect(s.title, 'Aider');
    expect(s.field('auto_commits')!.type, AgentFieldType.bool);
    expect(s.field('map_tokens')!.max, 8192);
    expect(s.field('variant')!.allowCustom, isTrue);
    expect(s.field('yolo')!.warning, isTrue);
    expect(s.field('nope'), isNull);
    expect(
      AgentSchema.fromJson(jsonDecode(jsonEncode(s.toJson()))).toJson(),
      s.toJson(),
    );
    expect(s.toPrettyJson(), contains('\n  "fields"'));
    expect(AgentFieldType.parse('weird'), AgentFieldType.text);
    expect(const AgentSchema(name: 'x').title, 'x');
  });

  test('effective values fall back to the default', () {
    final f = AgentSchema.fromJson(_aider).field('auto_commits')!;
    expect(f.effective({}), isTrue);
    expect(f.effective({'auto_commits': false}), isFalse);
  });

  test('copyWith keeps or clears nullable attributes', () {
    final f = AgentSchema.fromJson(_aider).field('map_tokens')!;
    expect(f.copyWith(label: 'x').max, 8192);
    expect(f.copyWith(max: null).max, isNull);
    expect(f.copyWith(defaultValue: 5).defaultValue, 5);
    final s = AgentSchema.fromJson(_aider).copyWith(binary: 'aider2');
    expect(s.binary, 'aider2');
    expect(s.fields, hasLength(6));
  });

  test('the TOML snippet declares the agent with its schema inline', () {
    final toml = AgentSchema.fromJson(_aider).toTomlSnippet();
    expect(toml, startsWith('[ai.agents.aider]\nbinary = "aider"\n'));
    final json = RegExp(
      r"schema = '''\n(.*)\n'''",
      dotAll: true,
    ).firstMatch(toml)!.group(1)!;
    final decoded = jsonDecode(json) as Map<String, dynamic>;
    expect(decoded['name'], 'aider');
    expect(decoded.containsKey('binary'), isFalse);
  });

  test('a quote in a label cannot close the snippet\'s string', () {
    final s = AgentSchema.fromJson(_aider).copyWith(displayName: "it'''s");
    final toml = s.toTomlSnippet();
    final body = toml.split("'''\n")[1];
    expect(body, isNot(contains("'")));
    final json = body.substring(0, body.lastIndexOf('\n'));
    expect((jsonDecode(json) as Map)['display_name'], "it'''s");
  });

  test('catalog lists custom agents apart from built-in ones', () {
    final catalog = agentSchemaCatalog(
      custom: [AgentSchema.fromJson(_aider)],
      customAgentsApiEnabled: true,
    );
    expect(catalog.custom.map((s) => s.name), ['aider']);
    expect(catalog.byName('claude')!.builtin, isTrue);
    expect(catalog.byName('nope'), isNull);
    final parsed = AgentSchemaCatalog.fromJson({
      'schemas': [_aider],
      'custom_agents_api_enabled': true,
    });
    expect(parsed.customAgentsApiEnabled, isTrue);
    expect(parsed.schemas.single.name, 'aider');
  });

  test('every built-in schema the daemon embeds parses', () {
    final schemas = builtinAgentSchemas();
    expect(schemas.map((s) => s.name), containsAll(['claude', 'openrouter']));
    final danger = schemas
        .firstWhere((s) => s.name == 'claude')
        .field('dangerously_skip_perms')!;
    expect(danger.warning, isTrue);
  });

  group('validateCustomSchema', () {
    test('accepts a valid schema', () {
      expect(validateCustomSchema(AgentSchema.fromJson(_aider)), isEmpty);
    });

    List<String> errorsFor(
      Map<String, dynamic> field, {
      String name = 'tool',
    }) => validateCustomSchema(
      AgentSchema.fromJson({
        'name': name,
        'binary': 'tool',
        'fields': [field],
      }),
      builtinNames: {'claude'},
    );

    test('reports every rule', () {
      expect(
        errorsFor({
          'key': 'a',
          'type': 'bool',
          'label': 'A',
          'flag': '--a',
        }, name: 'Bad Name'),
        hasLength(1),
      );
      expect(
        errorsFor({
          'key': 'a',
          'type': 'bool',
          'label': 'A',
          'flag': '--a',
        }, name: 'claude'),
        hasLength(1),
      );
      expect(
        validateCustomSchema(
          const AgentSchema(name: 'tool', binary: '/bin/tool'),
        ),
        hasLength(1),
      );
      final cases = <Map<String, dynamic>>[
        {'key': 'Bad', 'type': 'bool', 'label': 'A', 'flag': '--a'},
        {'key': 'prompt', 'type': 'bool', 'label': 'A', 'flag': '--a'},
        {'key': 'a', 'type': 'bool', 'label': ' ', 'flag': '--a'},
        {'key': 'a', 'type': 'bool', 'label': 'A', 'flag': 'a'},
        {
          'key': 'a',
          'type': 'text',
          'label': 'A',
          'flag': '--a',
          'false_flag': '--b',
        },
        {
          'key': 'a',
          'type': 'bool',
          'label': 'A',
          'flag': '--a',
          'false_flag': 'b',
        },
        {
          'key': 'a',
          'type': 'bool',
          'label': 'A',
          'flag': '--a',
          'false_flag': '--a',
        },
        {'key': 'a', 'type': 'select', 'label': 'A', 'flag': '--a'},
        {
          'key': 'a',
          'type': 'select',
          'label': 'A',
          'flag': '--a',
          'options': ['-x'],
        },
        {'key': 'a', 'type': 'number', 'label': 'A', 'flag': '--a', 'min': -1},
        {
          'key': 'a',
          'type': 'number',
          'label': 'A',
          'flag': '--a',
          'min': 5,
          'max': 1,
        },
        {
          'key': 'a',
          'type': 'bool',
          'label': 'A',
          'flag': '--a',
          'warning': true,
          'default': true,
        },
        {'key': 'model', 'type': 'bool', 'label': 'A', 'flag': '--a'},
      ];
      for (final c in cases) {
        expect(errorsFor(c), isNotEmpty, reason: '$c');
      }
      final dup = validateCustomSchema(
        AgentSchema.fromJson({
          'name': 'tool',
          'binary': 'tool',
          'fields': [
            {'key': 'a', 'type': 'bool', 'label': 'A', 'flag': '--a'},
            {'key': 'a', 'type': 'bool', 'label': 'B', 'flag': '--a'},
          ],
        }),
      );
      expect(dup, hasLength(2));
    });
  });
}
