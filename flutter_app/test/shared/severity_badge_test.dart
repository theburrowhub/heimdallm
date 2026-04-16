import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/widgets/severity_badge.dart';

void main() {
  testWidgets('SeverityBadge shows correct color for high', (tester) async {
    await tester.pumpWidget(
      const MaterialApp(home: Scaffold(body: SeverityBadge(severity: 'high'))),
    );
    final container = tester.widget<Container>(find.byType(Container).first);
    final decoration = container.decoration as BoxDecoration;
    expect(decoration.color, Colors.red.shade700);
  });

  testWidgets('SeverityBadge shows correct color for medium', (tester) async {
    await tester.pumpWidget(
      const MaterialApp(home: Scaffold(body: SeverityBadge(severity: 'medium'))),
    );
    final container = tester.widget<Container>(find.byType(Container).first);
    final decoration = container.decoration as BoxDecoration;
    expect(decoration.color, Colors.orange.shade700);
  });

  testWidgets('SeverityBadge shows correct color for low', (tester) async {
    await tester.pumpWidget(
      const MaterialApp(home: Scaffold(body: SeverityBadge(severity: 'low'))),
    );
    final container = tester.widget<Container>(find.byType(Container).first);
    final decoration = container.decoration as BoxDecoration;
    expect(decoration.color, Colors.green.shade700);
  });
}
