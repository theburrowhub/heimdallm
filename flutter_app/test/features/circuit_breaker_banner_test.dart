import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/features/circuit_breaker/circuit_breaker_banner.dart';

void main() {
  testWidgets('CircuitBreakerBanner shows the message and dismisses', (
    tester,
  ) async {
    var dismissed = false;
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: CircuitBreakerBanner(
            message: 'org/r #42 — per-PR cap reached',
            onDismiss: () => dismissed = true,
          ),
        ),
      ),
    );

    expect(find.textContaining('org/r #42'), findsOneWidget);
    expect(find.textContaining('per-PR cap reached'), findsOneWidget);

    await tester.tap(find.text('Dismiss'));
    await tester.pumpAndSettle();
    expect(dismissed, isTrue);
  });
}
