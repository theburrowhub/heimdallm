import 'package:flutter/material.dart';

import '../../core/models/config_model.dart';
import 'override_field.dart';

/// Title and one-line explanation of each token-saving measure, keyed by its
/// TOML key.
const tokenSavingMeasureText = <String, (String, String)>{
  'incremental_diff': (
    'Incremental re-reviews',
    'On a re-review, send only the commits pushed since the last review '
        '(full diff after a force-push or a merge from the base branch).',
  ),
  'filter_noise': (
    'Skip noise files',
    'Leave lockfiles, vendored and built output, binaries and generated '
        'code out of the diff.',
  ),
  'compact_prompt': (
    'Compact prompt',
    'Shorter instructions, trimmed comments, no bot chatter, capped '
        're-review context.',
  ),
  'limit_exploration': (
    'Limit agent exploration',
    'Cap Claude at 20 turns and medium effort unless the agent sets its own.',
  ),
};

/// Global editor: one switch per measure plus the noise glob list.
class TokenSavingSettingsEditor extends StatefulWidget {
  final TokenSavingSettings value;
  final ValueChanged<TokenSavingSettings> onChanged;

  const TokenSavingSettingsEditor({
    super.key,
    required this.value,
    required this.onChanged,
  });

  @override
  State<TokenSavingSettingsEditor> createState() =>
      _TokenSavingSettingsEditorState();
}

class _TokenSavingSettingsEditorState extends State<TokenSavingSettingsEditor> {
  late final TextEditingController _globs;

  @override
  void initState() {
    super.initState();
    _globs = TextEditingController(text: widget.value.noiseGlobs.join('\n'));
  }

  @override
  void dispose() {
    _globs.dispose();
    super.dispose();
  }

  static List<String> _parseGlobs(String text) => text
      .split('\n')
      .map((l) => l.trim())
      .where((l) => l.isNotEmpty)
      .toList();

  TokenSavingSettings _withMeasure(String key, bool v) => switch (key) {
    'incremental_diff' => widget.value.copyWith(incrementalDiff: v),
    'filter_noise' => widget.value.copyWith(filterNoise: v),
    'compact_prompt' => widget.value.copyWith(compactPrompt: v),
    _ => widget.value.copyWith(limitExploration: v),
  };

  @override
  Widget build(BuildContext context) {
    final helper = Theme.of(context).textTheme.bodySmall;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        for (final key in tokenSavingMeasureKeys)
          SwitchListTile(
            key: ValueKey('token-saving-$key'),
            contentPadding: EdgeInsets.zero,
            dense: true,
            title: Text(tokenSavingMeasureText[key]!.$1),
            subtitle: Text(tokenSavingMeasureText[key]!.$2, style: helper),
            value: widget.value.measure(key),
            onChanged: (v) => widget.onChanged(_withMeasure(key, v)),
          ),
        if (widget.value.filterNoise) ...[
          const SizedBox(height: 8),
          TextFormField(
            key: const ValueKey('token-saving-noise-globs'),
            controller: _globs,
            minLines: 3,
            maxLines: 8,
            decoration: const InputDecoration(
              labelText: 'Files to skip (one glob per line)',
              helperText:
                  '**/ matches any folder; a glob without "/" matches the '
                  'file name anywhere. Generated files and binaries are '
                  'always skipped.',
              helperMaxLines: 2,
              border: OutlineInputBorder(),
              isDense: true,
            ),
            onChanged: (v) => widget.onChanged(
              widget.value.copyWith(noiseGlobs: _parseGlobs(v)),
            ),
          ),
          Align(
            alignment: Alignment.centerRight,
            child: TextButton(
              key: const ValueKey('token-saving-restore-globs'),
              onPressed: () {
                _globs.text = defaultNoiseGlobs.join('\n');
                widget.onChanged(
                  widget.value.copyWith(noiseGlobs: defaultNoiseGlobs),
                );
              },
              child: const Text('Restore defaults'),
            ),
          ),
        ],
      ],
    );
  }
}

/// Org/repo editor: each measure inherits or is forced on/off here.
class TokenSavingOverrideEditor extends StatelessWidget {
  final TokenSavingOverride? value;

  /// Effective value of each measure one level up, for the "inherit" label.
  final bool Function(String key) inheritedValue;

  /// Where each inherited value comes from ('global' when null).
  final String Function(String key)? inheritedLabelFor;
  final ValueChanged<TokenSavingOverride?> onChanged;

  /// Clears one measure's override (a DELETE of `token_saving/<key>`).
  final ValueChanged<String> onReset;

  const TokenSavingOverrideEditor({
    super.key,
    required this.value,
    required this.inheritedValue,
    required this.onChanged,
    required this.onReset,
    this.inheritedLabelFor,
  });

  static String _label(bool v) => v ? 'on' : 'off';

  @override
  Widget build(BuildContext context) {
    final current = value ?? const TokenSavingOverride();
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        for (final key in tokenSavingMeasureKeys) ...[
          OverrideDropdown(
            key: ValueKey('token-saving-override-$key'),
            label: tokenSavingMeasureText[key]!.$1,
            globalValue: _label(inheritedValue(key)),
            inheritedLabel: inheritedLabelFor?.call(key) ?? 'global',
            overrideValue: current.measure(key) == null
                ? null
                : _label(current.measure(key)!),
            options: const ['on', 'off'],
            onChanged: (v) {
              final next = current.withMeasure(
                key,
                v == null ? null : v == 'on',
              );
              onChanged(next.isEmpty ? null : next);
            },
            onReset: () => onReset(key),
          ),
          const SizedBox(height: 10),
        ],
      ],
    );
  }
}
