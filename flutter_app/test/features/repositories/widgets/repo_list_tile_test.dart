import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/features/repositories/widgets/repo_list_tile.dart';
import 'package:heimdallm/features/repositories/widgets/feature_led.dart';

Widget _host(Widget child) =>
    MaterialApp(home: Scaffold(body: child));

void main() {
  const appConfig = AppConfig(
    serverPort: 1,
    pollInterval: '60s',
    retentionDays: 30,
    aiPrimary: 'claude',
    aiFallback: '',
    reviewMode: 'single',
    repoConfigs: {
      'theburrowhub/heimdallm': RepoConfig(prEnabled: true),
    },
    issueTracking: IssueTrackingConfig(),
  );

  testWidgets('shows 3 LEDs with correct states', (tester) async {
    await tester.pumpWidget(_host(RepoListTile(
      repo: 'theburrowhub/heimdallm',
      config: const RepoConfig(prEnabled: true, localDir: '/tmp/heimdallm'),
      appConfig: appConfig,
      selected: false,
      showNew: false,
      onSelectionToggle: () {},
      onTap: () {},
    )));
    expect(find.byType(FeatureLed), findsNWidgets(4));
  });

  testWidgets('tapping checkbox calls onSelectionToggle', (tester) async {
    var toggled = false;
    await tester.pumpWidget(_host(RepoListTile(
      repo: 'a/b',
      config: const RepoConfig(prEnabled: true),
      appConfig: appConfig,
      selected: false,
      showNew: false,
      onSelectionToggle: () => toggled = true,
      onTap: () {},
    )));
    await tester.tap(find.byKey(const Key('RepoListTile_checkbox')));
    expect(toggled, isTrue);
  });

  testWidgets('selected=true renders selected background', (tester) async {
    await tester.pumpWidget(_host(RepoListTile(
      repo: 'a/b',
      config: const RepoConfig(prEnabled: true),
      appConfig: appConfig,
      selected: true,
      showNew: false,
      onSelectionToggle: () {},
      onTap: () {},
    )));
    final card = tester.widget<Card>(find.byType(Card));
    expect(card.color, isNotNull);
  });

  testWidgets('shows NEW badge when showNew=true', (tester) async {
    await tester.pumpWidget(_host(RepoListTile(
      repo: 'a/b',
      config: RepoConfig(
        prEnabled: true,
        firstSeenAt: DateTime.now(),
      ),
      appConfig: appConfig,
      selected: false,
      showNew: true,
      onSelectionToggle: () {},
      onTap: () {},
    )));
    expect(find.text('NEW'), findsOneWidget);
  });

  testWidgets('hides NEW badge when showNew=false', (tester) async {
    await tester.pumpWidget(_host(RepoListTile(
      repo: 'a/b',
      config: const RepoConfig(prEnabled: true),
      appConfig: appConfig,
      selected: false,
      showNew: false,
      onSelectionToggle: () {},
      onTap: () {},
    )));
    expect(find.text('NEW'), findsNothing);
  });
}
