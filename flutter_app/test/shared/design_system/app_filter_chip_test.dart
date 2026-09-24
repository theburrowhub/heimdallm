import 'package:flutter/material.dart';
import 'package:flutter/semantics.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/design_system/components/components.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/design_system/tokens.dart';

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
  testWidgets('AppFilterChip fires onTap and renders icon/count', (
    tester,
  ) async {
    var tapped = false;
    await tester.pumpWidget(
      _hosted(
        AppFilterChip(
          label: 'Open',
          selected: false,
          icon: Icons.check,
          count: 3,
          onTap: () => tapped = true,
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Open'), findsOneWidget);
    expect(find.text('3'), findsOneWidget);
    expect(find.byIcon(Icons.check), findsOneWidget);

    await tester.tap(find.byType(AppFilterChip));
    await tester.pump();
    expect(tapped, isTrue);
  });

  testWidgets('AppFilterChip renders selected and unselected without error', (
    tester,
  ) async {
    await tester.pumpWidget(
      _hosted(
        Column(
          children: [
            AppFilterChip(label: 'PR', selected: true, onTap: () {}),
            AppFilterChip(
              label: 'MT',
              selected: false,
              accent: AppColors.featureMergeTracking,
              onTap: () {},
            ),
          ],
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('PR'), findsOneWidget);
    expect(find.text('MT'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('AppFilterChip announces selection and stays activatable via the '
      'accessibility tap action', (tester) async {
    // Regression test for two review rounds: swapping Material's
    // FilterChip for InkWell first dropped the "selected" announcement
    // (fixed with an outer Semantics(selected:)), and that fix then
    // dropped the tap action entirely, because
    // InkWell(excludeFromSemantics: true) removes its own
    // SemanticsAction.tap along with its default semantics — the outer
    // Semantics must declare its own onTap or a screen reader's
    // activation gesture stops reaching the chip at all.
    final handle = tester.ensureSemantics();
    var tapped = false;

    await tester.pumpWidget(
      _hosted(
        Column(
          children: [
            AppFilterChip(label: 'Open', selected: true, onTap: () {}),
            AppFilterChip(
              label: 'Closed',
              selected: false,
              onTap: () => tapped = true,
            ),
          ],
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(
      tester.getSemantics(find.text('Open')),
      matchesSemantics(
        label: 'Open',
        isButton: true,
        isSelected: true,
        hasSelectedState: true,
        hasTapAction: true,
        hasFocusAction: true,
        isFocusable: true,
      ),
    );
    expect(
      tester.getSemantics(find.text('Closed')),
      matchesSemantics(
        label: 'Closed',
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
    final closedNode = tester.getSemantics(find.text('Closed'));
    // ignore: deprecated_member_use
    tester.binding.pipelineOwner.semanticsOwner!.performAction(
      closedNode.id,
      SemanticsAction.tap,
    );
    await tester.pump();
    expect(tapped, isTrue);

    handle.dispose();
  });
}
