import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/models/review.dart';
import 'package:heimdallm/features/config/config_providers.dart'
    show computeGlobalDiffForTest;
import 'package:heimdallm/features/repositories/repo_diff.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/widgets/token_saving_editor.dart';

Widget _hosted(Widget child) => MaterialApp(
  theme: HeimdallmTheme.light(),
  home: HeimdallmTheme.scope(
    child: Scaffold(body: SingleChildScrollView(child: child)),
  ),
);

void main() {
  group('models', () {
    test('global settings default on and parse the daemon projection', () {
      expect(const TokenSavingSettings().measure('incremental_diff'), isTrue);
      final s = TokenSavingSettings.fromJson({
        'incremental_diff': false,
        'filter_noise': true,
        'compact_prompt': false,
        'limit_exploration': true,
        'noise_globs': ['a/**', 1],
      });
      expect(s.incrementalDiff, isFalse);
      expect(s.compactPrompt, isFalse);
      expect(s.noiseGlobs, ['a/**']);
      expect(s.measure('unknown'), isFalse);
      expect(TokenSavingSettings.fromJson({}).noiseGlobs, defaultNoiseGlobs);
      final cfg = AppConfig.fromJson({
        'token_saving': {'compact_prompt': false},
      });
      expect(cfg.tokenSaving.compactPrompt, isFalse);
      expect(AppConfig.fromJson({}).tokenSaving.compactPrompt, isTrue);
    });

    test('overrides parse only what is set', () {
      expect(TokenSavingOverride.maybeFromJson(null), isNull);
      expect(TokenSavingOverride.maybeFromJson(<String, dynamic>{}), isNull);
      final o = TokenSavingOverride.maybeFromJson({
        'filter_noise': false,
        'noise_globs': ['x'],
      })!;
      expect(o.measure('filter_noise'), isFalse);
      expect(o.measure('compact_prompt'), isNull);
      expect(o.noiseGlobs, ['x']);
      expect(o.withMeasure('filter_noise', null).measures, isEmpty);
      expect(
        o ==
            TokenSavingOverride.maybeFromJson({
              'filter_noise': false,
              'noise_globs': ['x'],
            }),
        isTrue,
      );
      expect(o.hashCode, isA<int>());
      expect(
        o == const TokenSavingOverride(
          measures: {'filter_noise': false},
          noiseGlobs: ['x', 'y'],
        ),
        isFalse,
      );
      expect(
        o == const TokenSavingOverride(
          measures: {'filter_noise': false},
          noiseGlobs: ['z'],
        ),
        isFalse,
      );

      final cfg = AppConfig.fromJson({
        'repositories': ['acme/api'],
        'repo_overrides': {
          'acme/api': {
            'token_saving': {'compact_prompt': false},
          },
        },
        'org_overrides': {
          'acme': {
            'token_saving': {'incremental_diff': true},
          },
        },
      });
      expect(
        cfg.repoConfigs['acme/api']!.tokenSaving!.measure('compact_prompt'),
        isFalse,
      );
      expect(
        cfg.orgConfigs['acme']!.tokenSaving!.measure('incremental_diff'),
        isTrue,
      );
      expect(cfg.orgConfigs['acme']!.hasOverride, isTrue);
    });

    test('diffs emit changed measures and glob lists', () {
      const base = AppConfig();
      final diff = computeGlobalDiffForTest(
        base,
        base.copyWith(
          tokenSaving: const TokenSavingSettings(
            compactPrompt: false,
            noiseGlobs: ['only/**'],
          ),
        ),
      );
      expect(diff['ai']['token_saving'], {
        'compact_prompt': false,
        'noise_globs': ['only/**'],
      });
      expect(
        computeGlobalDiffForTest(base, base)['ai']?['token_saving'],
        isNull,
      );

      const none = RepoConfig();
      final some = RepoConfig(
        tokenSaving: const TokenSavingOverride(
          measures: {'filter_noise': false},
        ),
      );
      expect(computeRepoDiff(none, some)['token_saving'], {
        'filter_noise': false,
      });
      expect(computeRepoDiff(some, none).containsKey('token_saving'), isFalse);
      expect(diffTokenSavingOverride(null, null), isEmpty);
    });

    test('review token usage label', () {
      final r = Review.fromJson({
        'id': 1,
        'pr_id': 1,
        'cli_used': 'claude',
        'summary': 's',
        'issues': [],
        'severity': 'low',
        'created_at': '2026-10-08T10:00:00Z',
        'input_tokens': 12345,
        'output_tokens': 450,
        'cache_read_tokens': 2000000,
        'cost_usd': 0.0042,
      });
      expect(r.hasTokenUsage, isTrue);
      expect(
        r.tokenUsageLabel,
        '12.3k in · 450 out tokens · 2.0M cached · \$0.0042',
      );
      final legacy = Review.fromJson({
        'id': 2,
        'pr_id': 1,
        'cli_used': 'codex',
        'summary': 's',
        'issues': [],
        'severity': 'low',
        'created_at': '2026-10-08T10:00:00Z',
      });
      expect(legacy.hasTokenUsage, isFalse);
      final est = Review.fromJson({
        ...legacy.toJson(),
        'input_tokens': 900,
        'output_tokens': 10,
        'tokens_estimated': true,
        'cost_usd': 1.5,
      });
      expect(est.tokenUsageLabel, '≈ 900 in · 10 out tokens · \$1.50');
    });
  });

  group('TokenSavingSettingsEditor', () {
    testWidgets('toggles measures and edits globs', (tester) async {
      var value = const TokenSavingSettings();
      await tester.pumpWidget(
        StatefulBuilder(
          builder: (context, setState) => _hosted(
            TokenSavingSettingsEditor(
              value: value,
              onChanged: (v) => setState(() => value = v),
            ),
          ),
        ),
      );
      await tester.tap(
        find.byKey(const ValueKey('token-saving-compact_prompt')),
      );
      await tester.pump();
      expect(value.compactPrompt, isFalse);

      await tester.enterText(
        find.byKey(const ValueKey('token-saving-noise-globs')),
        ' a/** \n\nb.lock\n',
      );
      expect(value.noiseGlobs, ['a/**', 'b.lock']);

      await tester.tap(
        find.byKey(const ValueKey('token-saving-restore-globs')),
      );
      await tester.pump();
      expect(value.noiseGlobs, defaultNoiseGlobs);

      await tester.tap(find.byKey(const ValueKey('token-saving-filter_noise')));
      await tester.pump();
      expect(value.filterNoise, isFalse);
      expect(
        find.byKey(const ValueKey('token-saving-noise-globs')),
        findsNothing,
      );

      for (final key in ['incremental_diff', 'limit_exploration']) {
        await tester.tap(find.byKey(ValueKey('token-saving-$key')));
        await tester.pump();
      }
      expect(value.incrementalDiff, isFalse);
      expect(value.limitExploration, isFalse);
    });
  });

  group('TokenSavingOverrideEditor', () {
    testWidgets('shows inherited values and resets one measure', (
      tester,
    ) async {
      TokenSavingOverride? changed;
      String? reset;
      await tester.pumpWidget(
        _hosted(
          TokenSavingOverrideEditor(
            value: const TokenSavingOverride(
              measures: {'compact_prompt': false},
            ),
            inheritedValue: (key) => key != 'filter_noise',
            inheritedLabelFor: (key) =>
                key == 'incremental_diff' ? 'org: acme' : 'global',
            onChanged: (v) => changed = v,
            onReset: (key) => reset = key,
          ),
        ),
      );
      expect(find.text('Compact prompt'), findsOneWidget);
      // Only incremental_diff is inherited from the org; the rest show global.
      expect(find.text('org: acme'), findsOneWidget);
      expect(find.text('overridden'), findsOneWidget);
      await tester.tap(find.text('\u00d7 reset'));
      expect(reset, 'compact_prompt');

      // Force filter_noise on: the override gains a measure.
      final filter = find.byKey(
        const ValueKey('token-saving-override-filter_noise'),
      );
      await tester.tap(
        find.descendant(
          of: filter,
          matching: find.byType(DropdownButtonFormField<String?>),
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.text('on').last);
      await tester.pumpAndSettle();
      expect(changed?.measure('filter_noise'), isTrue);
      expect(changed?.measure('compact_prompt'), isFalse);

    });
  });
}
