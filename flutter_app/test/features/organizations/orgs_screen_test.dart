import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/organizations/orgs_screen.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

class _StaticConfigNotifier extends ConfigNotifier {
  _StaticConfigNotifier(this.config);

  final AppConfig config;

  @override
  Future<AppConfig> build() async => config;
}

class _ErrorConfigNotifier extends ConfigNotifier {
  @override
  Future<AppConfig> build() async => throw Exception('boom');
}

Widget _hosted(Widget child) {
  return MaterialApp(
    theme: HeimdallmTheme.light(),
    builder: (context, navigatorChild) =>
        HeimdallmTheme.scope(child: navigatorChild ?? const SizedBox.shrink()),
    home: Scaffold(body: child),
  );
}

Widget _routerHost(List<dynamic> overrides) {
  final router = GoRouter(
    routes: [
      GoRoute(
        path: '/',
        builder: (_, _) => const Scaffold(body: OrgsScreen()),
        routes: [
          GoRoute(
            path: '/orgs/:name',
            builder: (context, state) =>
                Scaffold(body: Text('Org ${state.pathParameters['name']}')),
          ),
        ],
      ),
    ],
  );

  return ProviderScope(
    overrides: overrides.cast(),
    child: MaterialApp.router(
      theme: HeimdallmTheme.light(),
      builder: (context, navigatorChild) => HeimdallmTheme.scope(
        child: navigatorChild ?? const SizedBox.shrink(),
      ),
      routerConfig: router,
    ),
  );
}

void main() {
  testWidgets('shows an error when config loading fails', (tester) async {
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          configNotifierProvider.overrideWith(_ErrorConfigNotifier.new),
        ],
        child: _hosted(const OrgsScreen()),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.textContaining('Could not load config'), findsOneWidget);
  });

  testWidgets('shows the empty state when no organizations are known', (
    tester,
  ) async {
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          configNotifierProvider.overrideWith(
            () => _StaticConfigNotifier(const AppConfig()),
          ),
        ],
        child: _hosted(const OrgsScreen()),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.textContaining('No organizations yet'), findsOneWidget);
  });

  testWidgets('lists organizations and opens their detail route', (
    tester,
  ) async {
    const config = AppConfig(
      repoConfigs: {
        'acme/heimdallm': RepoConfig(prEnabled: true),
        'beta/console': RepoConfig(prEnabled: false),
      },
      orgConfigs: {'acme': OrgConfig(aiPrimary: 'gemini')},
    );

    await tester.pumpWidget(
      _routerHost([
        configNotifierProvider.overrideWith(
          () => _StaticConfigNotifier(config),
        ),
      ]),
    );
    await tester.pumpAndSettle();

    expect(find.text('acme'), findsOneWidget);
    expect(find.text('beta'), findsOneWidget);
    expect(find.text('Custom overrides on global defaults'), findsOneWidget);
    expect(find.text('Inherits global defaults'), findsOneWidget);

    await tester.tap(find.text('acme'));
    await tester.pumpAndSettle();

    expect(find.text('Org acme'), findsOneWidget);
  });
}
