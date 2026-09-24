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
  group('ReviewPrompt presets', () {
    test('fromPreset carries the review instructions', () {
      final p = ReviewPrompt.fromPreset(ReviewPrompt.presets.first);
      expect(p.instructions, isNotEmpty);
      expect(p.hasPRReview, isTrue);
    });

    test('preset → toJson → fromJson round-trips the instructions', () {
      final original = ReviewPrompt.fromPreset(ReviewPrompt.presets[1]);
      final round = ReviewPrompt.fromJson(original.toJson());
      expect(round.instructions, equals(original.instructions));
      expect(round.focus, equals(original.focus));
    });

    test('every preset has instructions', () {
      for (final p in ReviewPrompt.presets) {
        expect(
          p.instructions,
          isNotEmpty,
          reason: '${p.id} must have instructions',
        );
      }
    });

    test('preset ids are unique', () {
      final ids = ReviewPrompt.presets.map((p) => p.id).toList();
      expect(
        ids.toSet().length,
        equals(ids.length),
        reason: 'preset ids must be unique, found duplicates: ${_dupes(ids)}',
      );
    });
  });

  testWidgets('AgentsScreen renders every review preset card', (tester) async {
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

    for (final preset in ReviewPrompt.presets) {
      expect(
        find.text(preset.name),
        findsOneWidget,
        reason: 'missing preset "${preset.name}"',
      );
    }
    // The screen covers PR review only — no per-pipeline tabs any more.
    expect(find.byType(TabBar), findsNothing);
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

  testWidgets('active banner names the active prompt or the built-in default', (
    tester,
  ) async {
    await _pumpAgentsScreen(tester);
    expect(find.text('Built-in default'), findsOneWidget);

    final active = ReviewPrompt.fromPreset(
      ReviewPrompt.presets.first,
    ).copyWith(isDefaultPr: true);
    await _pumpAgentsScreen(tester, prompts: [active]);
    expect(find.text('Built-in default'), findsNothing);
    expect(find.text('General Review'), findsWidgets);
    expect(find.text('ACTIVE'), findsOneWidget);
  });

  testWidgets('preset cards expose add, active, and activate states', (
    tester,
  ) async {
    final inactive = ReviewPrompt.fromPreset(ReviewPrompt.presets.first);
    final active = ReviewPrompt.fromPreset(
      ReviewPrompt.presets[1],
    ).copyWith(isDefaultPr: true);
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
    'custom prompt validates content, inserts placeholders, and saves the active flag',
    (tester) async {
      tester.platformDispatcher.textScaleFactorTestValue = 0.5;
      addTearDown(tester.platformDispatcher.clearTextScaleFactorTestValue);

      final api = await _pumpAgentsScreen(tester);

      await tester.tap(find.text('Custom').first);
      await tester.pumpAndSettle();

      expect(find.text('New Review Prompt'), findsOneWidget);
      await tester.enterText(
        find.byType(TextFormField).first,
        'Custom review prompt',
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

      await tester.tap(find.byType(Switch).last);
      await tester.pump();
      await tester.tap(find.text('Save'));
      await tester.pumpAndSettle();

      verify(
        () => api.upsertAgent(
          any(
            that: predicate<Map<String, dynamic>>(
              (json) =>
                  json['name'] == 'Custom review prompt' &&
                  json['focus'] == 'custom' &&
                  json['prompt'] == '{repo}' &&
                  json['is_default_pr'] == true,
            ),
          ),
        ),
      ).called(1);
    },
  );

  testWidgets('prompt tiles can be deleted after confirmation', (tester) async {
    final prompt = ReviewPrompt.fromPreset(
      ReviewPrompt.presets.first,
    ).copyWith(isDefaultPr: true);
    final api = await _pumpAgentsScreen(tester, prompts: [prompt]);
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

  testWidgets('an inactive prompt tile can be activated and edited', (
    tester,
  ) async {
    const prompt = ReviewPrompt(
      id: 'custom-1',
      name: 'Team prompt',
      instructions: 'Check our conventions',
    );
    // The editor dialog has a fixed-width layout the wide test font overflows.
    tester.platformDispatcher.textScaleFactorTestValue = 0.6;
    addTearDown(tester.platformDispatcher.clearTextScaleFactorTestValue);
    final api = await _pumpAgentsScreen(tester, prompts: [prompt]);
    final tile = find.ancestor(
      of: find.text('Team prompt'),
      matching: find.byType(ListTile),
    );

    await tester.tap(
      find.descendant(of: tile, matching: find.widgetWithText(TextButton, 'Activate')),
    );
    await tester.pumpAndSettle();
    final activated =
        verify(() => api.upsertAgent(captureAny())).captured.single
            as Map<String, dynamic>;
    expect(activated['id'], 'custom-1');
    expect(activated['is_default_pr'], isTrue);

    await tester.tap(
      find.descendant(of: tile, matching: find.byIcon(Icons.edit)),
    );
    await tester.pumpAndSettle();
    expect(find.text('Use as the active review prompt'), findsOneWidget);
  });

  group('active flag', () {
    test('toJson emits is_default_pr and no legacy is_default', () {
      const p = ReviewPrompt(
        id: 'x',
        name: 'X',
        isDefaultPr: true,
        instructions: 'pr',
      );
      final json = p.toJson();
      expect(json['is_default_pr'], isTrue);
      expect(
        json.containsKey('is_default'),
        isFalse,
        reason: 'legacy key must not be emitted',
      );
      expect(json.containsKey('is_default_issue'), isFalse);
      expect(json.containsKey('is_default_dev'), isFalse);
    });

    test('fromJson seeds the flag from legacy is_default', () {
      final p = ReviewPrompt.fromJson({
        'id': 'x',
        'name': 'X',
        'is_default': true,
        'instructions': 'pr',
      });
      expect(p.isDefaultPr, isTrue);
    });

    test('fromJson prefers is_default_pr over legacy is_default', () {
      final p = ReviewPrompt.fromJson({
        'id': 'x',
        'name': 'X',
        'is_default': true,
        'is_default_pr': false,
        'instructions': 'pr',
      });
      expect(p.isDefaultPr, isFalse);
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
