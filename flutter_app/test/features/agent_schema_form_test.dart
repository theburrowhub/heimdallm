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
          builder: (context, setState) => AgentSchemaForm(
            fields: _schema.fields,
            keyPrefix: 'tool',
            enabled: enabled,
            valueOf: (k) => values[k],
            onChanged: (k, v) => setState(() => values[k] = v),
          ),
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
}
