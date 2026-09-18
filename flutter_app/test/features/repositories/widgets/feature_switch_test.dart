import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/features/repositories/widgets/feature_switch.dart';
import 'package:heimdallm/features/repositories/widgets/feature_palette.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

Widget _host(Widget child) => MaterialApp(
  theme: HeimdallmTheme.light(),
  builder: (context, navigatorChild) =>
      HeimdallmTheme.scope(child: navigatorChild ?? const SizedBox.shrink()),
  home: Scaffold(body: Center(child: child)),
);

void main() {
  testWidgets('value=true: renders Switch with feature activeColor', (
    tester,
  ) async {
    await tester.pumpWidget(
      _host(
        FeatureSwitch(
          feature: Feature.prReview,
          value: true,
          onChanged: (_) {},
        ),
      ),
    );
    final sw = tester.widget<Switch>(find.byType(Switch));
    expect(sw.value, isTrue);
    expect(sw.activeThumbColor, FeaturePalette.prReview);
  });

  testWidgets('value=false: renders Switch off', (tester) async {
    await tester.pumpWidget(
      _host(
        FeatureSwitch(
          feature: Feature.develop,
          value: false,
          onChanged: (_) {},
        ),
      ),
    );
    expect(tester.widget<Switch>(find.byType(Switch)).value, isFalse);
  });

  testWidgets('value=null: renders mixed placeholder (no Switch, MIXED key)', (
    tester,
  ) async {
    await tester.pumpWidget(
      _host(
        FeatureSwitch(
          feature: Feature.issueTracking,
          value: null,
          onChanged: (_) {},
        ),
      ),
    );
    expect(find.byType(Switch), findsNothing);
    expect(find.byKey(const Key('FeatureSwitch_mixed')), findsOneWidget);
  });

  testWidgets('tap when value=null calls onChanged(true)', (tester) async {
    bool? lastValue;
    await tester.pumpWidget(
      _host(
        FeatureSwitch(
          feature: Feature.develop,
          value: null,
          onChanged: (v) => lastValue = v,
        ),
      ),
    );
    await tester.tap(find.byKey(const Key('FeatureSwitch_mixed')));
    expect(lastValue, isTrue);
  });
}
