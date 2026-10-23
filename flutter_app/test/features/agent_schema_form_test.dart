import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/agent_schema.dart';
import 'package:heimdallm/features/cli_agents/agent_schema_form.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

final _schema = AgentSchema.fromJson({
  'name': 'tool',
  'fields': [
    {
      'key': 'mode',
      'type': 'select',
      'label': 'Mode',
      'flag': '--mode',
      'options': ['fast', 'deep'],
      'default': 'fast',
      'description': 'How hard it works',
    },
    {
      'key': 'variant',
      'type': 'select',
      'label': 'Variant',
      'flag': '--variant',
      'options': ['a', 'b'],
      'allow_custom': true,
      'empty_label': 'CLI default',
    },
    {
      'key': 'depth',
      'type': 'number',
      'label': 'Depth',
      'flag': '--depth',
      'min': 1,
      'max': 10,
      'default': 3,
    },
    {'key': 'notes', 'type': 'text', 'label': 'Notes', 'flag': '--notes'},
    {
      'key': 'commits',
      'type': 'bool',
      'label': '--commits',
      'flag': '--commits',
      'default': true,
      'description': 'Commit edits',
    },
    {
      'key': 'yolo',
      'type': 'bool',
      'label': '--yolo',
      'flag': '--yolo',
      'warning': true,
    },
  ],
});

Future<Map<String, Object?>> _pump(
  WidgetTester tester, {
  Map<String, Object?> initial = const {},
  bool enabled = true,
  bool integerNumbers = false,
  Map<String, bool>? invalid,
  void Function(void Function(void Function()) rebuild)? onBuilt,
}) async {
  final values = {...initial};
  tester.view.physicalSize = const Size(1200, 800);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
  await tester.pumpWidget(
    MaterialApp(
      theme: HeimdallmTheme.light(),
      builder: (context, child) =>
          HeimdallmTheme.scope(child: child ?? const SizedBox.shrink()),
      home: Scaffold(
        body: StatefulBuilder(
          builder: (context, setState) {
            onBuilt?.call(setState);
            return AgentSchemaForm(
              fields: _schema.fields,
              keyPrefix: 'tool',
              enabled: enabled,
              integerNumbers: integerNumbers,
              valueOf: (k) => values[k],
              onChanged: (k, v) => setState(() => values[k] = v),
              onInvalidChanged: invalid == null
                  ? null
                  : (k, bad) => invalid[k] = bad,
            );
          },
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return values;
}

Finder _field(String key) => find.byKey(ValueKey('agent-field-tool-$key'));

void main() {
  testWidgets('renders each field type and reports edits', (tester) async {
    final values = await _pump(tester);
    expect(find.text('Default (fast)'), findsOneWidget);
    expect(find.text('Suggestions: a, b'), findsOneWidget);
    expect(find.text('default: 3'), findsOneWidget);

    await tester.tap(_field('mode'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('deep').last);
    await tester.pumpAndSettle();
    expect(values['mode'], 'deep');

    await tester.enterText(_field('variant'), ' custom-v ');
    expect(values['variant'], 'custom-v');
    await tester.enterText(_field('variant'), '');
    expect(values['variant'], isNull);

    await tester.enterText(_field('notes'), 'hello');
    expect(values['notes'], 'hello');

    // The toggle starts from its default (on) and turns off.
    expect(tester.widget<Switch>(_field('commits')).value, isTrue);
    await tester.tap(_field('commits'));
    await tester.pump();
    expect(values['commits'], isFalse);
  });

  testWidgets('a number outside its bounds is not reported', (tester) async {
    final values = await _pump(tester);
    await tester.enterText(_field('depth'), '50');
    await tester.pump();
    expect(values.containsKey('depth'), isFalse);
    expect(find.text('At most 10'), findsOneWidget);
    await tester.enterText(_field('depth'), '0');
    await tester.pump();
    expect(find.text('At least 1'), findsOneWidget);
    await tester.enterText(_field('depth'), '1.2.3');
    await tester.pump();
    expect(find.text('Not a number'), findsOneWidget);
    await tester.enterText(_field('depth'), '7');
    await tester.pump();
    expect(values['depth'], 7);
    await tester.enterText(_field('depth'), '');
    await tester.pump();
    expect(values['depth'], isNull);
  });

  testWidgets('a dangerous toggle can only be turned off', (tester) async {
    await _pump(tester);
    expect(tester.widget<Switch>(_field('yolo')).onChanged, isNull);
  });

  testWidgets('a dangerous toggle that is on can be turned off once', (
    tester,
  ) async {
    final values = await _pump(tester, initial: {'yolo': true});
    expect(tester.widget<Switch>(_field('yolo')).onChanged, isNotNull);
    await tester.tap(_field('yolo'));
    await tester.pump();
    expect(values['yolo'], isFalse);
    expect(tester.widget<Switch>(_field('yolo')).onChanged, isNull);
  });

  testWidgets('an unlisted value stays selectable', (tester) async {
    await _pump(tester, initial: {'mode': 'legacy'});
    expect(find.text('legacy (not listed)'), findsOneWidget);
  });

  testWidgets('a disabled form takes no edits', (tester) async {
    await _pump(tester, enabled: false);
    expect(tester.widget<Switch>(_field('commits')).onChanged, isNull);
  });

  testWidgets('a value of the wrong type does not break the form', (
    tester,
  ) async {
    await _pump(
      tester,
      initial: {
        'mode': 3,
        'notes': true,
        'depth': '7',
        'commits': 'false',
        'variant': [],
      },
    );
    expect(tester.takeException(), isNull);
    expect(find.text('3 (not listed)'), findsOneWidget);
    expect(find.text('true'), findsOneWidget);
    expect(find.text('7'), findsOneWidget);
    expect(tester.widget<Switch>(_field('commits')).value, isFalse);
    expect(asSettingNum([]), isNull);
    expect(asSettingBool('yes'), isNull);
    expect(asSettingBool('true'), isTrue);
  });

  testWidgets('invalid number input is reported, not its last valid prefix', (
    tester,
  ) async {
    final invalid = <String, bool>{};
    final values = await _pump(tester, invalid: invalid);
    // max is 10: "1" is valid, "15" is not.
    await tester.enterText(_field('depth'), '1');
    await tester.pump();
    expect(values['depth'], 1);
    await tester.enterText(_field('depth'), '15');
    await tester.pump();
    expect(invalid['depth'], isTrue);
    await tester.enterText(_field('depth'), '9');
    await tester.pump();
    expect(invalid['depth'], isFalse);
    expect(values['depth'], 9);
  });

  testWidgets('an explicit zero shows when the default is not zero', (
    tester,
  ) async {
    final shown = AgentSchemaForm(
      fields: [
        const AgentSchemaField(
          key: 'n',
          type: AgentFieldType.number,
          label: 'N',
          defaultValue: 3,
        ),
        const AgentSchemaField(
          key: 'z',
          type: AgentFieldType.number,
          label: 'Z',
        ),
      ],
      keyPrefix: 'p',
      valueOf: (k) => 0,
      onChanged: (_, _) {},
    );
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: HeimdallmTheme.scope(child: shown)),
      ),
    );
    expect(
      tester
          .widget<TextField>(
            find.descendant(
              of: find.byKey(const ValueKey('agent-field-p-n')),
              matching: find.byType(TextField),
            ),
          )
          .controller!
          .text,
      '0',
    );
    expect(
      tester
          .widget<TextField>(
            find.descendant(
              of: find.byKey(const ValueKey('agent-field-p-z')),
              matching: find.byType(TextField),
            ),
          )
          .controller!
          .text,
      '',
    );
  });

  testWidgets('integer fields refuse decimals', (tester) async {
    final invalid = <String, bool>{};
    final values = await _pump(tester, integerNumbers: true, invalid: invalid);
    await tester.enterText(_field('depth'), '2.5');
    await tester.pump();
    // The formatter drops the dot: 25 is over the maximum of 10.
    expect(values.containsKey('depth'), isFalse);
    expect(invalid['depth'], isTrue);
    await tester.enterText(_field('depth'), '4');
    await tester.pump();
    expect(values['depth'], 4);
    expect(values['depth'], isA<int>());
  });

  testWidgets('text fields follow a value changed from outside', (
    tester,
  ) async {
    late void Function(void Function()) rebuild;
    final values = await _pump(
      tester,
      initial: {'notes': 'before'},
      onBuilt: (r) => rebuild = r,
    );
    rebuild(() => values['notes'] = 'after');
    await tester.pump();
    expect(find.text('after'), findsOneWidget);
    // While the user types, an outside change does not overwrite the input.
    await tester.showKeyboard(_field('notes'));
    rebuild(() => values['notes'] = 'ignored');
    await tester.pump();
    expect(find.text('after'), findsOneWidget);
  });
}
