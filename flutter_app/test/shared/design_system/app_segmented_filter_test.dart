import 'package:flutter/material.dart';
import 'package:flutter/semantics.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/design_system/components/components.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

Widget _hosted(Widget child) {
  return MaterialApp(
    theme: HeimdallmTheme.light(),
    debugShowCheckedModeBanner: false,
    builder: (context, navigatorChild) =>
        HeimdallmTheme.scope(child: navigatorChild ?? const SizedBox.shrink()),
    home: Scaffold(body: child),
  );
}

void main() {
  testWidgets('AppSegmentedFilter renders every segment with its count', (
    tester,
  ) async {
    String? selected;
    await tester.pumpWidget(
      _hosted(
        AppSegmentedFilter<String>(
          segments: const [
            AppSegment(value: 'all', label: 'All', count: 151),
            AppSegment(value: 'monitored', label: 'Monitored', count: 107),
            AppSegment(
              value: 'not_monitored',
              label: 'Not monitored',
              count: 44,
            ),
          ],
          current: 'all',
          onChanged: (v) => selected = v,
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('All'), findsOneWidget);
    expect(find.text('151'), findsOneWidget);
    expect(find.text('Monitored'), findsOneWidget);
    expect(find.text('107'), findsOneWidget);
    expect(find.text('Not monitored'), findsOneWidget);
    expect(find.text('44'), findsOneWidget);

    await tester.tap(find.text('Monitored'));
    await tester.pump();
    expect(selected, 'monitored');
  });

  testWidgets('AppSegmentedFilter renders without a count', (tester) async {
    await tester.pumpWidget(
      _hosted(
        AppSegmentedFilter<String>(
          segments: const [
            AppSegment(value: 'a', label: 'A'),
            AppSegment(value: 'b', label: 'B'),
          ],
          current: 'a',
          onChanged: (_) {},
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('A'), findsOneWidget);
    expect(find.text('B'), findsOneWidget);
  });

  testWidgets(
    'AppSegmentedFilter announces selection and stays activatable via the '
    'accessibility tap action',
    (tester) async {
      // Regression test for two review rounds: swapping Material's
      // SegmentedButton for InkWell first dropped the "selected"
      // announcement (fixed with an outer Semantics(selected:)), and that
      // fix then dropped the tap action entirely, because
      // InkWell(excludeFromSemantics: true) removes its own
      // SemanticsAction.tap along with its default semantics — the outer
      // Semantics must declare its own onTap or a screen reader's
      // activation gesture stops reaching a segment at all.
      final handle = tester.ensureSemantics();
      String? changedTo;

      await tester.pumpWidget(
        _hosted(
          AppSegmentedFilter<String>(
            segments: const [
              AppSegment(value: 'all', label: 'All'),
              AppSegment(value: 'monitored', label: 'Monitored'),
            ],
            current: 'all',
            onChanged: (v) => changedTo = v,
          ),
        ),
      );
      await tester.pumpAndSettle();

      expect(
        tester.getSemantics(find.text('All')),
        matchesSemantics(
          label: 'All',
          isButton: true,
          isSelected: true,
          hasSelectedState: true,
          hasTapAction: true,
          hasFocusAction: true,
          isFocusable: true,
        ),
      );
      expect(
        tester.getSemantics(find.text('Monitored')),
        matchesSemantics(
          label: 'Monitored',
          isButton: true,
          isSelected: false,
          hasSelectedState: true,
          hasTapAction: true,
          hasFocusAction: true,
          isFocusable: true,
        ),
      );

      // Activate via the accessibility action, not a raw touch — this is
      // exactly the path that silently stopped working when
      // excludeFromSemantics dropped InkWell's own SemanticsAction.tap.
      final monitoredNode = tester.getSemantics(find.text('Monitored'));
      // ignore: deprecated_member_use
      tester.binding.pipelineOwner.semanticsOwner!.performAction(
        monitoredNode.id,
        SemanticsAction.tap,
      );
      await tester.pump();

      expect(changedTo, 'monitored');

      handle.dispose();
    },
  );
}
