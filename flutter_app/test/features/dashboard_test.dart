import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallr/core/models/pr.dart';
import 'package:heimdallr/features/dashboard/dashboard_providers.dart';
import 'package:heimdallr/features/dashboard/dashboard_screen.dart';

void main() {
  testWidgets('DashboardScreen shows PR title', (tester) async {
    final pr = PR(
      id: 1, githubId: 101, repo: 'org/repo', number: 42,
      title: 'Fix critical bug', author: 'alice', url: 'https://github.com',
      state: 'open', updatedAt: DateTime.now(),
    );

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          prsProvider.overrideWith((ref) => Future.value([pr])),
          sseStreamProvider.overrideWith((ref) => const Stream.empty()),
        ],
        child: MaterialApp.router(
          routerConfig: GoRouter(
            routes: [GoRoute(path: '/', builder: (_, __) => const DashboardScreen())],
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Fix critical bug'), findsOneWidget);
    expect(find.text('org/repo'), findsOneWidget);
  });

  testWidgets('DashboardScreen shows loading indicator while fetching', (tester) async {
    final completer = Completer<List<PR>>();
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          prsProvider.overrideWith((ref) => completer.future),
          sseStreamProvider.overrideWith((ref) => const Stream.empty()),
        ],
        child: MaterialApp.router(
          routerConfig: GoRouter(
            routes: [GoRoute(path: '/', builder: (_, __) => const DashboardScreen())],
          ),
        ),
      ),
    );
    await tester.pump();
    expect(find.byType(CircularProgressIndicator), findsOneWidget);
  });
}
