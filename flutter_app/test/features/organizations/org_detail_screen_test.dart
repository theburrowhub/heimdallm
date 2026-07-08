import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/agent.dart';
import 'package:heimdallm/features/agents/agents_screen.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/organizations/org_detail_screen.dart';
import 'package:mocktail/mocktail.dart';

class MockApiClient extends Mock implements ApiClient {}

void main() {
  testWidgets('OrgDetailScreen exposes pipeline org overrides', (tester) async {
    final mockApi = MockApiClient();
    when(() => mockApi.fetchConfig()).thenAnswer(
      (_) async => {
        'repositories': <String>[],
        'server_port': 1,
        'poll_interval': '60s',
        'retention_days': 30,
        'ai_primary': 'claude',
        'ai_fallback': '',
        'review_mode': 'single',
        'issue_tracking': {'enabled': false},
        'triage_owner': 'global-owner',
        'clone_dir': '/work/global',
        'auto_promote_triage': false,
        'auto_promote_refinement': false,
        'generate_pr_description': false,
        'org_overrides': {
          'acme': {
            'triage_owner': 'alice',
            'clone_dir': '/work/acme',
            'auto_promote_triage': true,
            'auto_promote_refinement': true,
            'generate_pr_description': true,
            'never_approve_with_issues': true,
            'issue_tracking': {
              'organizations': ['acme'],
              'assignees': ['alice'],
            },
          },
        },
      },
    );

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          apiClientProvider.overrideWithValue(mockApi),
          configNotifierProvider.overrideWith(ConfigNotifier.new),
          agentsProvider.overrideWith((_) async => <ReviewPrompt>[]),
        ],
        child: const MaterialApp(home: OrgDetailScreen(orgName: 'acme')),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Pipeline'), findsOneWidget);
    expect(find.text('Triage owner'), findsOneWidget);
    expect(find.text('Clone directory'), findsOneWidget);
    expect(find.text('Auto-promote triage'), findsOneWidget);
    expect(find.text('Auto-promote refinement'), findsOneWidget);
    expect(find.text('Refinement labels'), findsOneWidget);
    expect(find.text('Generate PR description'), findsOneWidget);
    expect(find.text('Never approve PRs with issues'), findsOneWidget);
    // "Organizations" now names both the new review-policy section header and
    // the Issue Tracking org filter; target the filter by its unique helper.
    expect(find.text('GitHub org names to filter issues'), findsOneWidget);
    expect(find.text('Assignees'), findsOneWidget);
  });
}
