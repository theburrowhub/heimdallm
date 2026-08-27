import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/widgets/type_badge.dart';

void main() {
  testWidgets('renders the development badge', (tester) async {
    await tester.pumpWidget(
      const MaterialApp(
        home: Scaffold(body: TypeBadge(type: 'dev')),
      ),
    );

    expect(find.text('DEV'), findsOneWidget);
  });

  testWidgets('renders the fallback badge for an unknown type', (tester) async {
    await tester.pumpWidget(
      const MaterialApp(
        home: Scaffold(body: TypeBadge(type: 'unknown')),
      ),
    );

    expect(find.text('?'), findsOneWidget);
  });
}
