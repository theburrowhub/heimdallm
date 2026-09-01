import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/features/repositories/widgets/bulk_actions_bar.dart';
import 'package:heimdallm/features/repositories/widgets/feature_palette.dart';

Widget _host(Widget child) =>
    MaterialApp(home: Scaffold(body: child));

void main() {
  testWidgets('renders "N selected" and one switch per feature', (tester) async {
    await tester.pumpWidget(_host(BulkActionsBar(
      selectedCount: 3,
      aggregates: const {
        Feature.prReview: true,
        Feature.issueTracking: null,
        Feature.develop: false,
        Feature.mergeTracking: false,
      },
      onApply: (_, _) {},
      onClear: () {},
    )));
    expect(find.text('3 selected'), findsOneWidget);
    expect(find.byType(Switch), findsNWidgets(3));   // three pure states
    expect(find.byKey(const Key('FeatureSwitch_mixed')), findsOneWidget);
  });

  testWidgets('MIXED pill only shown for mixed features', (tester) async {
    await tester.pumpWidget(_host(BulkActionsBar(
      selectedCount: 2,
      aggregates: const {
        Feature.prReview: true,
        Feature.issueTracking: null,
        Feature.develop: false,
        Feature.mergeTracking: false,
      },
      onApply: (_, _) {},
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
        Feature.mergeTracking: false,
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
        Feature.mergeTracking: true,
      },
      onApply: (_, _) {},
      onClear: () => cleared = true,
    )));
    await tester.tap(find.text('Clear'));
    expect(cleared, isTrue);
  });

  group('instance routing row', () {
    testWidgets('is hidden on a single-daemon install', (tester) async {
      // With one daemon there is nothing to route between, so the control
      // would be pure clutter.
      await tester.pumpWidget(
        _host(
          BulkActionsBar(
            selectedCount: 2,
            aggregates: const {
              Feature.prReview: true,
              Feature.issueTracking: false,
              Feature.develop: false,
              Feature.mergeTracking: false,
            },
            onApply: (_, _) {},
            onClear: () {},
          ),
        ),
      );
      expect(find.text('Route to instance'), findsNothing);
    });

    testWidgets('routes the selection to a chosen instance', (tester) async {
      String? assigned;
      var called = false;
      await tester.pumpWidget(
        _host(
          BulkActionsBar(
            selectedCount: 2,
            aggregates: const {
              Feature.prReview: true,
              Feature.issueTracking: false,
              Feature.develop: false,
              Feature.mergeTracking: false,
            },
            onApply: (_, _) {},
            onClear: () {},
            instances: const [
              (id: 'hub-1', name: 'Local hub'),
              (id: 'srv-a', name: 'Server A'),
            ],
            onAssignInstance: (id) {
              assigned = id;
              called = true;
            },
          ),
        ),
      );

      expect(find.text('Route to instance'), findsOneWidget);
      await tester.tap(find.text('Choose…'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Server A'));
      await tester.pumpAndSettle();

      expect(called, isTrue);
      expect(assigned, 'srv-a');
    });

    testWidgets('inherit clears the rules', (tester) async {
      String? assigned = 'sentinel';
      await tester.pumpWidget(
        _host(
          BulkActionsBar(
            selectedCount: 1,
            aggregates: const {
              Feature.prReview: true,
              Feature.issueTracking: false,
              Feature.develop: false,
              Feature.mergeTracking: false,
            },
            onApply: (_, _) {},
            onClear: () {},
            instances: const [(id: 'hub-1', name: 'Local hub')],
            onAssignInstance: (id) => assigned = id,
          ),
        ),
      );

      await tester.tap(find.text('Choose…'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Inherit (default instance)'));
      await tester.pumpAndSettle();

      // Null means "remove the rules", not "route to nothing".
      expect(assigned, isNull);
    });
  });
}
