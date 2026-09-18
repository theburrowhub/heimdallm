import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/agent.dart';
import 'package:heimdallm/features/agents/agents_screen.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:mocktail/mocktail.dart';

class _MockApiClient extends Mock implements ApiClient {}

Widget _hosted(Widget child) {
  return MaterialApp(
    theme: HeimdallmTheme.light(),
    builder: (context, navigatorChild) =>
        HeimdallmTheme.scope(child: navigatorChild ?? const SizedBox.shrink()),
    home: Scaffold(body: child),
  );
}

Future<_MockApiClient> _pumpAgentsScreen(
  WidgetTester tester, {
  List<ReviewPrompt> prompts = const [],
  Object? fetchError,
  Size size = const Size(1200, 900),
}) async {
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);

  final api = _MockApiClient();
  if (fetchError != null) {
    when(() => api.fetchAgents()).thenThrow(fetchError);
  } else {
    when(
      () => api.fetchAgents(),
    ).thenAnswer((_) async => prompts.map((p) => p.toJson()).toList());
  }
  when(() => api.upsertAgent(any())).thenAnswer((_) async {});
  when(() => api.deleteAgent(any())).thenAnswer((_) async {});

  await tester.pumpWidget(
    ProviderScope(
      overrides: [apiClientProvider.overrideWithValue(api)],
      child: _hosted(const AgentsScreen()),
    ),
  );
  await tester.pumpAndSettle();
  return api;
}

void main() {
  group('ReviewPrompt.fromPreset', () {
    test('PR review preset populates `instructions`, not the others', () {
      final p = ReviewPrompt.fromPreset(ReviewPrompt.presets.first);
      expect(p.instructions, isNotEmpty);
      expect(p.issueInstructions, isEmpty);
      expect(p.implementInstructions, isEmpty);
    });

    test(
      'issue-triage preset populates `issueInstructions`, not the others',
      () {
        final p = ReviewPrompt.fromPreset(
          ReviewPrompt.issueTriagePresets.first,
        );
        expect(p.issueInstructions, isNotEmpty);
        expect(p.instructions, isEmpty);
        expect(p.implementInstructions, isEmpty);
      },
    );

    test(
      'development preset populates `implementInstructions`, not the others',
      () {
        final p = ReviewPrompt.fromPreset(
          ReviewPrompt.developmentPresets.first,
        );
        expect(p.implementInstructions, isNotEmpty);
        expect(p.instructions, isEmpty);
        expect(p.issueInstructions, isEmpty);
      },
    );

    test(
      'preset → toJson → fromJson round-trips category-specific content',
      () {
        final original = ReviewPrompt.fromPreset(
          ReviewPrompt.developmentPresets[1],
        );
        final round = ReviewPrompt.fromJson(original.toJson());
        expect(
          round.implementInstructions,
          equals(original.implementInstructions),
        );
        expect(round.issueInstructions, equals(original.issueInstructions));
        expect(round.instructions, equals(original.instructions));
      },
    );
  });

  group('preset lists', () {
    test('every PR-review preset has only `instructions` populated', () {
      for (final p in ReviewPrompt.presets) {
        expect(
          p.instructions,
          isNotEmpty,
          reason: '${p.id} must have instructions',
        );
        expect(
          p.issueInstructions,
          isEmpty,
          reason: '${p.id} leaks into issueInstructions',
        );
        expect(
          p.implementInstructions,
          isEmpty,
          reason: '${p.id} leaks into implementInstructions',
        );
      }
    });

    test(
      'every issue-triage preset has only `issueInstructions` populated',
      () {
        for (final p in ReviewPrompt.issueTriagePresets) {
          expect(
            p.issueInstructions,
            isNotEmpty,
            reason: '${p.id} must have issueInstructions',
          );
          expect(
            p.instructions,
            isEmpty,
            reason: '${p.id} leaks into instructions',
          );
          expect(
            p.implementInstructions,
            isEmpty,
            reason: '${p.id} leaks into implementInstructions',
          );
        }
      },
    );

    test(
      'every development preset has only `implementInstructions` populated',
      () {
        for (final p in ReviewPrompt.developmentPresets) {
          expect(
            p.implementInstructions,
            isNotEmpty,
            reason: '${p.id} must have implementInstructions',
          );
          expect(
            p.instructions,
            isEmpty,
            reason: '${p.id} leaks into instructions',
          );
          expect(
            p.issueInstructions,
            isEmpty,
            reason: '${p.id} leaks into issueInstructions',
          );
        }
      },
    );

    test('preset ids are unique across all three categories', () {
      final all = [
        ...ReviewPrompt.presets,
        ...ReviewPrompt.issueTriagePresets,
        ...ReviewPrompt.developmentPresets,
      ];
      final ids = all.map((p) => p.id).toList();
      expect(
        ids.toSet().length,
        equals(ids.length),
        reason: 'preset ids must be unique, found duplicates: ${_dupes(ids)}',
      );
    });
  });

  testWidgets('AgentsScreen renders preset cards for every tab', (
    tester,
  ) async {
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          agentsProvider.overrideWith(
            (ref) => Future.value(const <ReviewPrompt>[]),
          ),
        ],
        child: _hosted(const AgentsScreen()),
      ),
    );
    await tester.pumpAndSettle();

    // Default selected tab is PR Review — its 5 presets should be visible
    for (final preset in ReviewPrompt.presets) {
      expect(
        find.text(preset.name),
        findsOneWidget,
        reason: 'PR Review tab missing "${preset.name}"',
      );
    }

    final issueTriageTab = find.descendant(
      of: find.byType(TabBar),
      matching: find.text('Issue Triage'),
    );
    expect(issueTriageTab, findsOneWidget);
    await tester.tap(issueTriageTab);
    await tester.pumpAndSettle();
    for (final preset in ReviewPrompt.issueTriagePresets) {
      expect(
        find.text(preset.name),
        findsOneWidget,
        reason: 'Issue Triage tab missing "${preset.name}"',
      );
    }

    // Switch to Development and assert its 5 presets render.
    final developmentTab = find.descendant(
      of: find.byType(TabBar),
      matching: find.text('Development'),
    );
    expect(developmentTab, findsOneWidget);
    await tester.tap(developmentTab);
    await tester.pumpAndSettle();
    for (final preset in ReviewPrompt.developmentPresets) {
      expect(
        find.text(preset.name),
        findsOneWidget,
        reason: 'Development tab missing "${preset.name}"',
      );
    }
  });

  testWidgets('custom PR prompt marks extra flags as CLI-specific', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(1200, 900);
    tester.view.devicePixelRatio = 1;
    // This assertion targets the CLI hint, not the dialog's fixed-width layout.
    tester.platformDispatcher.textScaleFactorTestValue = 0.6;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    addTearDown(tester.platformDispatcher.clearTextScaleFactorTestValue);

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          agentsProvider.overrideWith(
            (ref) => Future.value(const <ReviewPrompt>[]),
          ),
        ],
        child: _hosted(const AgentsScreen()),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.text('Custom').first);
    await tester.pumpAndSettle();

    final decorator = tester.widget<InputDecorator>(
      find.byWidgetPredicate(
        (widget) =>
            widget is InputDecorator &&
            widget.decoration.labelText == 'Extra CLI flags (optional)',
      ),
    );
    final hint = decorator.decoration.hintText!;
    expect(hint, contains('configured CLI'));
    expect(hint, isNot(contains('--')));
  });

  testWidgets('shows an error when prompts fail to load', (tester) async {
    await _pumpAgentsScreen(tester, fetchError: Exception('boom'));

    expect(find.text('Error: Exception: boom'), findsOneWidget);
  });

  testWidgets(
    'active banner renders built-in defaults on wide and compact layouts',
    (tester) async {
      final prompts = [
        ReviewPrompt.fromPreset(
          ReviewPrompt.presets.first,
        ).withActive(PromptCategory.prReview, true),
        ReviewPrompt.fromPreset(
          ReviewPrompt.developmentPresets.first,
        ).withActive(PromptCategory.development, true),
      ];

      await _pumpAgentsScreen(tester, prompts: prompts);
      expect(find.text('Built-in default'), findsOneWidget);
      expect(find.text('General Review'), findsWidgets);
      expect(find.text('Plan First'), findsWidgets);

      await _pumpAgentsScreen(
        tester,
        prompts: prompts,
        size: const Size(600, 900),
      );
      expect(find.text('Built-in default'), findsOneWidget);
      expect(find.text('PR Review'), findsWidgets);
      expect(find.text('Issue Triage'), findsWidgets);
      expect(find.text('Development'), findsWidgets);
    },
  );

  testWidgets('preset cards expose add, active, and activate states', (
    tester,
  ) async {
    final inactive = ReviewPrompt.fromPreset(ReviewPrompt.presets.first);
    final active = ReviewPrompt.fromPreset(
      ReviewPrompt.presets[1],
    ).withActive(PromptCategory.prReview, true);
    final api = await _pumpAgentsScreen(tester, prompts: [inactive, active]);

    expect(find.text('Tap to add'), findsWidgets);
    expect(find.text('Tap to activate'), findsOneWidget);
    expect(find.text('Active'), findsOneWidget);

    await tester.tap(find.text('General Review').first);
    await tester.pumpAndSettle();

    verify(
      () => api.upsertAgent(
        any(
          that: predicate<Map<String, dynamic>>(
            (json) =>
                json['id'] == inactive.id && json['is_default_pr'] == true,
          ),
        ),
      ),
    ).called(1);
  });

  testWidgets(
    'custom issue prompt validates content, inserts placeholders, and saves toggles',
    (tester) async {
      tester.platformDispatcher.textScaleFactorTestValue = 0.5;
      addTearDown(tester.platformDispatcher.clearTextScaleFactorTestValue);

      final api = await _pumpAgentsScreen(tester);

      final issueTriageTab = find.descendant(
        of: find.byType(TabBar),
        matching: find.text('Issue Triage'),
      );
      await tester.tap(issueTriageTab);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Custom').first);
      await tester.pumpAndSettle();

      expect(find.text('New Issue Triage Prompt'), findsOneWidget);
      await tester.enterText(
        find.byType(TextFormField).first,
        'Issue custom prompt',
      );

      await tester.tap(find.text('Save'));
      await tester.pumpAndSettle();
      expect(
        find.text('Please provide instructions or a template'),
        findsOneWidget,
      );

      await tester.tap(find.text('Advanced (full template)'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('{repo}'));
      await tester.pumpAndSettle();

      final focusDropdown = find.byWidgetPredicate(
        (widget) =>
            widget is DropdownButtonFormField<String> &&
            widget.decoration.labelText == 'Focus',
      );
      await tester.tap(focusDropdown);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Custom').last);
      await tester.pumpAndSettle();

      await tester.tap(find.byType(Switch).at(1));
      await tester.pump();
      await tester.tap(find.byType(Switch).last);
      await tester.pump();
      await tester.tap(find.text('Save'));
      await tester.pumpAndSettle();

      verify(
        () => api.upsertAgent(
          any(
            that: predicate<Map<String, dynamic>>(
              (json) =>
                  json['name'] == 'Issue custom prompt' &&
                  json['focus'] == 'custom' &&
                  json['issue_prompt'] == '{repo}' &&
                  json['is_default_issue'] == true &&
                  json['is_default_dev'] == true,
            ),
          ),
        ),
      ).called(1);
    },
  );

  testWidgets('development prompt tiles can be deleted after confirmation', (
    tester,
  ) async {
    final prompt = ReviewPrompt.fromPreset(
      ReviewPrompt.developmentPresets.first,
    ).copyWith(isDefaultDev: true);
    final api = await _pumpAgentsScreen(tester, prompts: [prompt]);

    final developmentTab = find.descendant(
      of: find.byType(TabBar),
      matching: find.text('Development'),
    );
    await tester.tap(developmentTab);
    await tester.pumpAndSettle();
    expect(find.text('ACTIVE'), findsOneWidget);

    await tester.tap(find.byIcon(Icons.delete).first);
    await tester.pumpAndSettle();
    expect(find.text('Remove prompt?'), findsOneWidget);
    await tester.tap(find.text('Cancel'));
    await tester.pumpAndSettle();
    verifyNever(() => api.deleteAgent(any()));

    await tester.tap(find.byIcon(Icons.delete).first);
    await tester.pumpAndSettle();
    await tester.tap(find.text('Remove'));
    await tester.pumpAndSettle();

    verify(() => api.deleteAgent(prompt.id)).called(1);
  });

  group('per-category activation', () {
    test('withActive flips only the targeted flag', () {
      const p = ReviewPrompt(
        id: 'x',
        name: 'X',
        instructions: 'pr',
        issueInstructions: 'issue',
        implementInstructions: 'dev',
      );
      final pr = p.withActive(PromptCategory.prReview, true);
      expect(pr.isDefaultPr, isTrue);
      expect(pr.isDefaultIssue, isFalse);
      expect(pr.isDefaultDev, isFalse);

      final both = pr.withActive(PromptCategory.development, true);
      expect(both.isDefaultPr, isTrue, reason: 'PR flag preserved');
      expect(both.isDefaultDev, isTrue);
      expect(both.isDefaultIssue, isFalse);
    });

    test('toJson emits per-category flags and no legacy is_default', () {
      const p = ReviewPrompt(
        id: 'x',
        name: 'X',
        isDefaultPr: true,
        isDefaultDev: true,
        instructions: 'pr',
        implementInstructions: 'dev',
      );
      final json = p.toJson();
      expect(json['is_default_pr'], isTrue);
      expect(json['is_default_issue'], isFalse);
      expect(json['is_default_dev'], isTrue);
      expect(
        json.containsKey('is_default'),
        isFalse,
        reason: 'legacy key must not be emitted',
      );
    });

    test('fromJson seeds all three flags from legacy is_default', () {
      final json = {
        'id': 'x',
        'name': 'X',
        'is_default': true,
        'instructions': 'pr',
      };
      final p = ReviewPrompt.fromJson(json);
      expect(p.isDefaultPr, isTrue);
      expect(p.isDefaultIssue, isTrue);
      expect(p.isDefaultDev, isTrue);
    });

    test('fromJson prefers per-category flags over legacy is_default', () {
      final json = {
        'id': 'x',
        'name': 'X',
        'is_default': true,
        'is_default_pr': false,
        'is_default_issue': true,
        'is_default_dev': false,
        'instructions': 'pr',
      };
      final p = ReviewPrompt.fromJson(json);
      expect(p.isDefaultPr, isFalse);
      expect(p.isDefaultIssue, isTrue);
      expect(p.isDefaultDev, isFalse);
    });
  });
}

List<String> _dupes(List<String> ids) {
  final seen = <String>{};
  final dupes = <String>{};
  for (final id in ids) {
    if (!seen.add(id)) dupes.add(id);
  }
  return dupes.toList();
}
