import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/github_principal.dart';
import 'package:heimdallm/shared/widgets/principal_chips_field.dart';

void main() {
  const suggestions = [
    GitHubPrincipal('acme/core', isTeam: true),
    GitHubPrincipal('alice'),
    GitHubPrincipal('alicia'),
  ];

  Future<List<List<String>>> pump(
    WidgetTester tester, {
    List<String> value = const [],
  }) async {
    final changes = <List<String>>[];
    var current = value;
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: Column(
            children: [
              StatefulBuilder(
                builder: (context, setState) => PrincipalChipsField(
                  label: 'Exempt',
                  value: current,
                  suggestions: suggestions,
                  onChanged: (v) => setState(() {
                    current = v;
                    changes.add(v);
                  }),
                ),
              ),
              const TextField(key: ValueKey('elsewhere')),
            ],
          ),
        ),
      ),
    );
    return changes;
  }

  Finder field() => find.widgetWithText(TextField, 'Exempt');

  testWidgets('picking a suggestion adds it as a chip', (tester) async {
    final changes = await pump(tester);
    await tester.enterText(field(), 'cor');
    await tester.pumpAndSettle();
    expect(find.byIcon(Icons.group), findsOneWidget); // the team suggestion

    await tester.tap(find.text('acme/core'));
    await tester.pumpAndSettle();
    expect(changes.last, ['acme/core']);
    expect(find.widgetWithText(InputChip, 'acme/core'), findsOneWidget);
  });

  testWidgets('Enter picks the highlighted match, or keeps unknown text', (
    tester,
  ) async {
    final changes = await pump(tester);
    await tester.enterText(field(), 'ali');
    await tester.pumpAndSettle();
    await tester.testTextInput.receiveAction(TextInputAction.done);
    await tester.pumpAndSettle();
    expect(changes.last, ['alice']);

    await tester.enterText(field(), 'renovate[bot]');
    await tester.testTextInput.receiveAction(TextInputAction.done);
    await tester.pumpAndSettle();
    expect(changes.last, ['alice', 'renovate[bot]']);
  });

  testWidgets('a comma splits pasted text and skips duplicates', (
    tester,
  ) async {
    final changes = await pump(tester, value: const ['alice']);
    await tester.enterText(field(), 'Alice, bob, bob,');
    await tester.pump();
    expect(changes.single, ['alice', 'bob']);
  });

  testWidgets('leaving the field commits what was typed', (tester) async {
    final changes = await pump(tester);
    await tester.enterText(field(), 'zed');
    await tester.pump();
    expect(changes, isEmpty);

    await tester.tap(find.byKey(const ValueKey('elsewhere')));
    await tester.pumpAndSettle();
    expect(changes.single, ['zed']);
  });

  testWidgets('a chip can be removed', (tester) async {
    final changes = await pump(tester, value: const ['alice', 'acme/core']);
    await tester.tap(find.byTooltip('Remove alice'));
    await tester.pump();
    expect(changes.single, ['acme/core']);
  });
}
