import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/features/repositories/widgets/repo_grid_tile.dart';
import 'package:heimdallm/features/repositories/widgets/feature_led.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

Widget _host(Widget child) => MaterialApp(
  theme: HeimdallmTheme.light(),
  builder: (context, navigatorChild) =>
      HeimdallmTheme.scope(child: navigatorChild ?? const SizedBox.shrink()),
  home: Scaffold(body: SizedBox(width: 200, height: 180, child: child)),
);

void main() {
  const appConfig = AppConfig(
    serverPort: 1,
    pollInterval: '60s',
    retentionDays: 30,
    aiPrimary: 'claude',
    aiFallback: '',
    reviewMode: 'single',
    repoConfigs: {'a/repo': RepoConfig(prEnabled: true)},
  );

  testWidgets('shows repo name, org subtitle, 2 LEDs', (tester) async {
    await tester.pumpWidget(
      _host(
        RepoGridTile(
          repo: 'a/repo',
          config: const RepoConfig(prEnabled: true),
          appConfig: appConfig,
          selected: false,
          showNew: false,
          onSelectionToggle: () {},
          onTap: () {},
        ),
      ),
    );
    expect(find.text('repo'), findsOneWidget);
    expect(find.text('a'), findsOneWidget);
    expect(find.byType(FeatureLed), findsNWidgets(2));
  });

  testWidgets('tapping tile (outside checkbox) calls onTap', (tester) async {
    var tapped = false;
    await tester.pumpWidget(
      _host(
        RepoGridTile(
          repo: 'a/repo',
          config: const RepoConfig(prEnabled: true),
          appConfig: appConfig,
          selected: false,
          showNew: false,
          onSelectionToggle: () {},
          onTap: () => tapped = true,
        ),
      ),
    );
    await tester.tap(find.text('repo'));
    expect(tapped, isTrue);
  });

  testWidgets('tapping the checkbox toggles selection', (tester) async {
    var toggled = false;
    await tester.pumpWidget(
      _host(
        RepoGridTile(
          repo: 'a/repo',
          config: const RepoConfig(prEnabled: true),
          appConfig: appConfig,
          selected: false,
          showNew: false,
          onSelectionToggle: () => toggled = true,
          onTap: () {},
        ),
      ),
    );

    await tester.tap(find.byKey(const Key('RepoGridTile_checkbox')));
    await tester.pump();

    expect(toggled, isTrue);
  });

  testWidgets('selected tiles show their checkmark and new badge', (
    tester,
  ) async {
    await tester.pumpWidget(
      _host(
        RepoGridTile(
          repo: 'a/repo',
          config: const RepoConfig(prEnabled: false),
          appConfig: appConfig,
          selected: true,
          showNew: true,
          onSelectionToggle: () {},
          onTap: () {},
        ),
      ),
    );

    expect(find.byIcon(Icons.check), findsOneWidget);
    expect(find.text('NEW'), findsOneWidget);
  });
}
