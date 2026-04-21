import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/repositories/repos_screen.dart';
import 'package:heimdallm/features/repositories/widgets/bulk_actions_bar.dart';
import '../../core/platform/fake_platform_services.dart';

Widget _host(AppConfig cfg) => ProviderScope(
      overrides: [
        platformServicesProvider.overrideWithValue(FakePlatformServices()),
        configNotifierProvider.overrideWith(() => _FakeConfig(cfg)),
      ],
      child: const MaterialApp(home: Scaffold(body: ReposScreen())),
    );

class _FakeConfig extends ConfigNotifier {
  _FakeConfig(this.initial);
  final AppConfig initial;
  @override
  Future<AppConfig> build() async => initial;
  @override
  Future<void> save(AppConfig next) async {
    state = AsyncData(next);
  }
}

AppConfig _cfg() => const AppConfig(
      serverPort: 1,
      pollInterval: '60s',
      retentionDays: 30,
      aiPrimary: 'claude',
      aiFallback: '',
      reviewMode: 'single',
      repoConfigs: {
        'a/one': RepoConfig(prEnabled: true),
        'a/two': RepoConfig(prEnabled: true),
      },
      issueTracking: IssueTrackingConfig(),
    );

void main() {
  testWidgets('bulk bar appears when a repo is selected', (tester) async {
    await tester.pumpWidget(_host(_cfg()));
    await tester.pumpAndSettle();

    expect(find.byType(BulkActionsBar), findsNothing);

    await tester.tap(find.byKey(const Key('RepoListTile_checkbox')).first);
    await tester.pump();

    expect(find.byType(BulkActionsBar), findsOneWidget);
    expect(find.text('1 selected'), findsOneWidget);
  });

  testWidgets('Clear dismisses the bulk bar', (tester) async {
    await tester.pumpWidget(_host(_cfg()));
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('RepoListTile_checkbox')).first);
    await tester.pump();
    await tester.tap(find.text('Clear'));
    await tester.pump();

    expect(find.byType(BulkActionsBar), findsNothing);
  });

  testWidgets('selecting Monitored hides non-monitored repos',
      (tester) async {
    final cfg = const AppConfig(
      serverPort: 1, pollInterval: '60s', retentionDays: 30,
      aiPrimary: 'claude', aiFallback: '', reviewMode: 'single',
      repoConfigs: {
        'a/one': RepoConfig(prEnabled: true),
        'a/two': RepoConfig(prEnabled: false),
      },
      issueTracking: IssueTrackingConfig(),
    );
    await tester.pumpWidget(_host(cfg));
    await tester.pumpAndSettle();

    expect(find.text('a/one'), findsOneWidget);
    expect(find.text('a/two'), findsOneWidget);

    await tester.tap(find.text('Monitored'));
    await tester.pumpAndSettle();

    expect(find.text('a/one'), findsOneWidget);
    expect(find.text('a/two'), findsNothing);
  });
}
