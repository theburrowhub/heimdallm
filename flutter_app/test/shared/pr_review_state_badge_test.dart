import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/widgets/pr_review_state_badge.dart';

void main() {
  testWidgets('renders every supported review state', (tester) async {
    final states = <String, ({String label, Color color})>{
      'APPROVED': (label: 'PR APPROVED', color: Colors.green.shade700),
      'CHANGES_REQUESTED': (
        label: 'CHANGES REQUESTED',
        color: Colors.red.shade700,
      ),
      'COMMENTED': (label: 'PR COMMENTED', color: Colors.blue.shade700),
      'FIX_PUSHED': (label: 'FIX PUSHED', color: Colors.purple.shade700),
    };

    for (final entry in states.entries) {
      await tester.pumpWidget(
        MaterialApp(
          home: Scaffold(body: PRReviewStateBadge(state: entry.key)),
        ),
      );

      expect(find.text(entry.value.label), findsOneWidget);
      final container = tester.widget<Container>(find.byType(Container).first);
      final decoration = container.decoration as BoxDecoration;
      expect(decoration.color, entry.value.color);
    }
  });

  testWidgets('renders nothing for an unsupported review state', (
    tester,
  ) async {
    await tester.pumpWidget(
      const MaterialApp(
        home: Scaffold(body: PRReviewStateBadge(state: 'PENDING')),
      ),
    );

    expect(find.byType(SizedBox), findsOneWidget);
    expect(find.byType(Container), findsNothing);
  });
}
