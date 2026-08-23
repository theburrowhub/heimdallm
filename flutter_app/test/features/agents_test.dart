import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/agent.dart';
import 'package:heimdallm/features/agents/agents_screen.dart';

void main() {
  group('ReviewPrompt.fromPreset', () {
    test('PR review preset populates `instructions`, not the others', () {
      final p = ReviewPrompt.fromPreset(ReviewPrompt.presets.first);
      expect(p.instructions, isNotEmpty);
      expect(p.issueInstructions, isEmpty);
      expect(p.implementInstructions, isEmpty);
    });

    test('issue-triage preset populates `issueInstructions`, not the others', () {
      final p = ReviewPrompt.fromPreset(ReviewPrompt.issueTriagePresets.first);
      expect(p.issueInstructions, isNotEmpty);
      expect(p.instructions, isEmpty);
      expect(p.implementInstructions, isEmpty);
    });

    test('development preset populates `implementInstructions`, not the others', () {
      final p = ReviewPrompt.fromPreset(ReviewPrompt.developmentPresets.first);
      expect(p.implementInstructions, isNotEmpty);
      expect(p.instructions, isEmpty);
      expect(p.issueInstructions, isEmpty);
    });

    test('preset → toJson → fromJson round-trips category-specific content', () {
      final original = ReviewPrompt.fromPreset(ReviewPrompt.developmentPresets[1]);
      final round = ReviewPrompt.fromJson(original.toJson());
      expect(round.implementInstructions, equals(original.implementInstructions));
      expect(round.issueInstructions, equals(original.issueInstructions));
      expect(round.instructions, equals(original.instructions));
    });
  });

  group('preset lists', () {
    test('every PR-review preset has only `instructions` populated', () {
      for (final p in ReviewPrompt.presets) {
        expect(p.instructions, isNotEmpty, reason: '${p.id} must have instructions');
        expect(p.issueInstructions, isEmpty, reason: '${p.id} leaks into issueInstructions');
        expect(p.implementInstructions, isEmpty, reason: '${p.id} leaks into implementInstructions');
      }
    });

    test('every issue-triage preset has only `issueInstructions` populated', () {
      for (final p in ReviewPrompt.issueTriagePresets) {
        expect(p.issueInstructions, isNotEmpty, reason: '${p.id} must have issueInstructions');
        expect(p.instructions, isEmpty, reason: '${p.id} leaks into instructions');
        expect(p.implementInstructions, isEmpty, reason: '${p.id} leaks into implementInstructions');
      }
    });

    test('every development preset has only `implementInstructions` populated', () {
      for (final p in ReviewPrompt.developmentPresets) {
        expect(p.implementInstructions, isNotEmpty, reason: '${p.id} must have implementInstructions');
        expect(p.instructions, isEmpty, reason: '${p.id} leaks into instructions');
        expect(p.issueInstructions, isEmpty, reason: '${p.id} leaks into issueInstructions');
      }
    });

    test('preset ids are unique across all three categories', () {
      final all = [
        ...ReviewPrompt.presets,
        ...ReviewPrompt.issueTriagePresets,
        ...ReviewPrompt.developmentPresets,
      ];
      final ids = all.map((p) => p.id).toList();
      expect(ids.toSet().length, equals(ids.length),
          reason: 'preset ids must be unique, found duplicates: ${_dupes(ids)}');
    });
  });

  testWidgets('AgentsScreen renders preset cards for every tab', (tester) async {
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          agentsProvider.overrideWith((ref) => Future.value(const <ReviewPrompt>[])),
        ],
        child: const MaterialApp(home: Scaffold(body: AgentsScreen())),
      ),
    );
    await tester.pumpAndSettle();

    // Default selected tab is PR Review — its 5 presets should be visible
    for (final preset in ReviewPrompt.presets) {
      expect(find.text(preset.name), findsOneWidget,
          reason: 'PR Review tab missing "${preset.name}"');
    }

    final issueTriageTab = find.descendant(
      of: find.byType(TabBar),
      matching: find.text('Issue Triage'),
    );
    expect(issueTriageTab, findsOneWidget);
    await tester.tap(issueTriageTab);
    await tester.pumpAndSettle();
    for (final preset in ReviewPrompt.issueTriagePresets) {
      expect(find.text(preset.name), findsOneWidget,
          reason: 'Issue Triage tab missing "${preset.name}"');
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
      expect(find.text(preset.name), findsOneWidget,
          reason: 'Development tab missing "${preset.name}"');
    }
  });

  group('per-category activation', () {
    test('withActive flips only the targeted flag', () {
      const p = ReviewPrompt(id: 'x', name: 'X',
          instructions: 'pr', issueInstructions: 'issue', implementInstructions: 'dev');
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
      const p = ReviewPrompt(id: 'x', name: 'X',
          isDefaultPr: true, isDefaultDev: true,
          instructions: 'pr', implementInstructions: 'dev');
      final json = p.toJson();
      expect(json['is_default_pr'], isTrue);
      expect(json['is_default_issue'], isFalse);
      expect(json['is_default_dev'], isTrue);
      expect(json.containsKey('is_default'), isFalse,
          reason: 'legacy key must not be emitted');
    });

    test('fromJson seeds all three flags from legacy is_default', () {
      final json = {
        'id': 'x', 'name': 'X',
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
        'id': 'x', 'name': 'X',
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
