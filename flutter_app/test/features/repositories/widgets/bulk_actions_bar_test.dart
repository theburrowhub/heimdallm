import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/features/repositories/widgets/bulk_actions_bar.dart';
import 'package:heimdallm/features/repositories/widgets/feature_palette.dart';

Widget _host(Widget child) =>
    MaterialApp(home: Scaffold(body: child));

void main() {
  testWidgets('renders "N selected" and 3 switches', (tester) async {
    await tester.pumpWidget(_host(BulkActionsBar(
      selectedCount: 3,
      aggregates: const {
        Feature.prReview: true,
        Feature.issueTracking: null,
        Feature.develop: false,
      },
      onApply: (_, __) {},
      onClear: () {},
    )));
    expect(find.text('3 selected'), findsOneWidget);
    expect(find.byType(Switch), findsNWidgets(2));   // two pure states
    expect(find.byKey(const Key('FeatureSwitch_mixed')), findsOneWidget);
  });

  testWidgets('MIXED pill only shown for mixed features', (tester) async {
    await tester.pumpWidget(_host(BulkActionsBar(
      selectedCount: 2,
      aggregates: const {
        Feature.prReview: true,
        Feature.issueTracking: null,
        Feature.develop: false,
      },
      onApply: (_, __) {},
      onClear: () {},
    )));
    expect(find.text('MIXED'), findsOneWidget);
  });

  testWidgets('flipping a switch calls onApply(feature, newValue)',
      (tester) async {
    Feature? calledFeature;
    bool? calledValue;
    await tester.pumpWidget(_host(BulkActionsBar(
      selectedCount: 3,
      aggregates: const {
        Feature.prReview: false,
        Feature.issueTracking: true,
        Feature.develop: false,
      },
      onApply: (f, v) { calledFeature = f; calledValue = v; },
      onClear: () {},
    )));
    await tester.tap(find.byType(Switch).first);
    await tester.pumpAndSettle();
    expect(calledFeature, Feature.prReview);
    expect(calledValue, isTrue);
  });

  testWidgets('tapping Clear calls onClear', (tester) async {
    var cleared = false;
    await tester.pumpWidget(_host(BulkActionsBar(
      selectedCount: 1,
      aggregates: const {
        Feature.prReview: true,
        Feature.issueTracking: true,
        Feature.develop: true,
      },
      onApply: (_, __) {},
      onClear: () => cleared = true,
    )));
    await tester.tap(find.text('Clear'));
    expect(cleared, isTrue);
  });
}
