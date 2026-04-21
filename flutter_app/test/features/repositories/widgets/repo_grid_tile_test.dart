import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/features/repositories/widgets/repo_grid_tile.dart';
import 'package:heimdallm/features/repositories/widgets/feature_led.dart';

Widget _host(Widget child) =>
    MaterialApp(home: Scaffold(body: SizedBox(width: 200, height: 180, child: child)));

void main() {
  final appConfig = const AppConfig(
    serverPort: 1, pollInterval: '60s', retentionDays: 30,
    aiPrimary: 'claude', aiFallback: '', reviewMode: 'single',
    repoConfigs: {
      'a/repo': RepoConfig(prEnabled: true),
    },
    issueTracking: IssueTrackingConfig(),
  );

  testWidgets('shows repo name, org subtitle, 3 LEDs', (tester) async {
    await tester.pumpWidget(_host(RepoGridTile(
      repo: 'a/repo',
      config: const RepoConfig(prEnabled: true),
      appConfig: appConfig,
      selected: false,
      onSelectionToggle: () {},
      onTap: () {},
    )));
    expect(find.text('repo'), findsOneWidget);
    expect(find.text('a'), findsOneWidget);
    expect(find.byType(FeatureLed), findsNWidgets(3));
  });

  testWidgets('tapping tile (outside checkbox) calls onTap', (tester) async {
    var tapped = false;
    await tester.pumpWidget(_host(RepoGridTile(
      repo: 'a/repo',
      config: const RepoConfig(prEnabled: true),
      appConfig: appConfig,
      selected: false,
      onSelectionToggle: () {},
      onTap: () => tapped = true,
    )));
    await tester.tap(find.text('repo'));
    expect(tapped, isTrue);
  });
}
