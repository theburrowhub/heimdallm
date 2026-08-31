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

Map<String, dynamic> _configJson({
  Map<String, dynamic> orgMergeTracking = const {},
}) => {
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
      'never_approve_min_severity': 'high',
      'issue_tracking': {
        'organizations': ['acme'],
        'assignees': ['alice'],
      },
    },
  },
  'merge_tracking': {
    'enabled': false,
    'enable_auto_merge': false,
    'update_branch': false,
    'resolve_conflicts': false,
    'merge': false,
    'merge_method': 'squash',
    'include_assigned': false,
    'require_approval': false,
    'poll_interval': '5m',
    'max_prs_per_tick': 20,
    'max_update_attempts': 3,
    'max_resolve_attempts': 2,
    'max_merge_attempts': 3,
    'action_cooldown': '10m',
    'resolve_timeout': '30m',
    'resolve_effort': 'high',
    'orgs': {'acme': orgMergeTracking},
  },
};

Future<MockApiClient> _pumpOrgDetail(
  WidgetTester tester, {
  Map<String, dynamic> orgMergeTracking = const {},
}) async {
  final mockApi = MockApiClient();
  final config = _configJson(orgMergeTracking: orgMergeTracking);
  when(() => mockApi.fetchConfig()).thenAnswer((_) async => config);
  when(
    () => mockApi.patchMergeTrackingOrgConfig('acme', any()),
  ).thenAnswer((_) async => config);

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
  return mockApi;
}

void main() {
  testWidgets('OrgDetailScreen exposes pipeline and merge-tracking overrides', (
    tester,
  ) async {
    await _pumpOrgDetail(tester);

    expect(find.text('Pipeline'), findsOneWidget);
    expect(find.text('Triage owner'), findsOneWidget);
    expect(find.text('Clone directory'), findsOneWidget);
    expect(find.text('Auto-promote triage'), findsOneWidget);
    expect(find.text('Auto-promote refinement'), findsOneWidget);
    expect(find.text('Refinement labels'), findsOneWidget);
    expect(find.text('Generate PR description'), findsOneWidget);
    expect(find.text('Never approve PRs with issues'), findsOneWidget);
    // The severity threshold sits next to the toggle it qualifies, and renders
    // the org's stored override rather than the global default.
    expect(find.text('Never approve — minimum severity'), findsOneWidget);
    expect(find.text('high'), findsWidgets);
    // "Organizations" now names both the review-policy section header and the
    // Issue Tracking org filter; target the filter by its unique helper.
    expect(find.text('GitHub org names to filter issues'), findsOneWidget);
    expect(find.text('Assignees'), findsOneWidget);

    expect(find.text('Merge Tracking'), findsOneWidget);
    expect(find.text('Track my pull requests'), findsOneWidget);
    expect(find.text('Include PRs assigned to me'), findsOneWidget);
    expect(find.text('Automations'), findsOneWidget);
    expect(find.text('Turn on auto-merge'), findsOneWidget);
    expect(find.text('Update stale branches'), findsOneWidget);
    expect(find.text('Resolve conflicts'), findsOneWidget);
    expect(find.text('Merge when ready'), findsOneWidget);
    expect(find.text('Require an approving review'), findsOneWidget);
    expect(find.text('Merge method'), findsOneWidget);
    expect(find.text('Conflict-resolution timeout'), findsOneWidget);
    expect(find.text('Conflict-resolution effort'), findsOneWidget);
    expect(find.text('Advanced limits'), findsOneWidget);
    expect(find.text('Maximum branch-update attempts'), findsOneWidget);
    expect(find.text('Maximum conflict-resolution attempts'), findsOneWidget);
    expect(find.text('Maximum merge attempts'), findsOneWidget);
    expect(find.text('Action cooldown'), findsOneWidget);
    expect(
      find.textContaining('Check interval and PRs per tick stay global'),
      findsOneWidget,
    );
  });

  testWidgets(
    'rapid merge-tracking edits are persisted in one complete patch',
    (tester) async {
      final mockApi = await _pumpOrgDetail(tester);

      tester
          .widget<Switch>(
            find.byKey(const Key('org_merge_tracking_enable_auto_merge')),
          )
          .onChanged!(true);
      await tester.pump();
      tester
          .widget<Switch>(
            find.byKey(const Key('org_merge_tracking_update_branch')),
          )
          .onChanged!(true);
      await tester.pump();

      verifyNever(() => mockApi.patchMergeTrackingOrgConfig('acme', any()));

      await tester.pump(const Duration(milliseconds: 801));
      await tester.pump();

      final captured = verify(
        () => mockApi.patchMergeTrackingOrgConfig('acme', captureAny()),
      ).captured;
      expect(captured, hasLength(1));
      expect(captured.single, {
        'enable_auto_merge': true,
        'update_branch': true,
      });
    },
  );

  testWidgets('reset sends null for one field without changing siblings', (
    tester,
  ) async {
    final mockApi = await _pumpOrgDetail(
      tester,
      orgMergeTracking: const {
        'enable_auto_merge': true,
        'update_branch': true,
      },
    );

    tester
        .widget<TextButton>(
          find.byKey(const Key('org_merge_tracking_enable_auto_merge_reset')),
        )
        .onPressed!();
    await tester.pump();

    expect(
      tester
          .widget<Switch>(
            find.byKey(const Key('org_merge_tracking_update_branch')),
          )
          .value,
      isTrue,
    );
    expect(
      find.byKey(const Key('org_merge_tracking_update_branch_reset')),
      findsOneWidget,
    );

    await tester.pump(const Duration(milliseconds: 801));
    await tester.pump();

    final captured = verify(
      () => mockApi.patchMergeTrackingOrgConfig('acme', captureAny()),
    ).captured;
    expect(captured, hasLength(1));
    expect(captured.single, {'enable_auto_merge': null});
  });
}
