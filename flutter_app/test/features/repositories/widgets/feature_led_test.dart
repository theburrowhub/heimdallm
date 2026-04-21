import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/features/repositories/widgets/feature_led.dart';
import 'package:heimdallm/features/repositories/widgets/feature_palette.dart';

Widget _host(Widget child) =>
    MaterialApp(home: Scaffold(body: Center(child: child)));

void main() {
  testWidgets('renders coloured dot when on', (tester) async {
    await tester.pumpWidget(_host(const FeatureLed(
      feature: Feature.prReview,
      isOn: true,
      sourceLine: 'Source: repo-level (prEnabled = true)',
    )));
    final container = tester.widget<Container>(find.byType(Container));
    final deco = container.decoration as BoxDecoration;
    expect(deco.color, FeaturePalette.prReview);
    expect(deco.border, isNull);
  });

  testWidgets('renders hollow grey when off', (tester) async {
    await tester.pumpWidget(_host(const FeatureLed(
      feature: Feature.develop,
      isOn: false,
      sourceLine: 'Source: disabled per-repo (devEnabled = false)',
    )));
    final container = tester.widget<Container>(find.byType(Container));
    final deco = container.decoration as BoxDecoration;
    expect(deco.color, FeaturePalette.offFill);
    expect(deco.border, isNotNull);
  });

  testWidgets('tooltip contains feature name, state, source line',
      (tester) async {
    await tester.pumpWidget(_host(const FeatureLed(
      feature: Feature.issueTracking,
      isOn: true,
      sourceLine: 'Source: inherited from global monitored list',
    )));
    final tip = tester.widget<Tooltip>(find.byType(Tooltip));
    expect(tip.message, contains('Issue Tracking'));
    expect(tip.message, contains('On'));
    expect(tip.message, contains('inherited from global'));
  });
}
