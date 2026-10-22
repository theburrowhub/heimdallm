import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/models/agent_schema.dart';
import '../../shared/design_system/components/components.dart';

/// Renders an agent's settings form from its schema: dropdowns for select
/// fields, switches for toggles, and text inputs for numbers and text. The
/// form holds no state of its own: [valueOf] reads the current value and
/// [onChanged] reports an edit (null means "not set").
class AgentSchemaForm extends StatelessWidget {
  final List<AgentSchemaField> fields;
  final Object? Function(String key) valueOf;
  final void Function(String key, Object? value) onChanged;

  /// Distinguishes the widgets of different agents (`agent-field-<prefix>-<key>`).
  final String keyPrefix;

  /// Label of the empty choice when a field names none.
  final String defaultEmptyLabel;

  /// Read-only preview (the custom agent editor).
  final bool enabled;

  const AgentSchemaForm({
    super.key,
    required this.fields,
    required this.valueOf,
    required this.onChanged,
    required this.keyPrefix,
    this.defaultEmptyLabel = 'CLI default',
    this.enabled = true,
  });

  @override
  Widget build(BuildContext context) {
    final inputs = [
      for (final f in fields)
        if (f.type != AgentFieldType.bool) f,
    ];
    final toggles = [
      for (final f in fields)
        if (f.type == AgentFieldType.bool) f,
    ];
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        if (inputs.isNotEmpty)
          AppFieldGrid(
            minFieldWidth: 220,
            children: [for (final f in inputs) _input(context, f)],
          ),
        if (inputs.isNotEmpty && toggles.isNotEmpty) const SizedBox(height: 10),
        if (toggles.isNotEmpty)
          Wrap(
            spacing: 24,
            runSpacing: 8,
            children: [for (final f in toggles) _toggle(f)],
          ),
      ],
    );
  }

  ValueKey<String> _key(AgentSchemaField f) =>
      ValueKey('agent-field-$keyPrefix-${f.key}');

  Widget _withTooltip(AgentSchemaField f, Widget child) => f.description.isEmpty
      ? child
      : Tooltip(
          message: f.description,
          waitDuration: const Duration(milliseconds: 600),
          child: child,
        );

  InputDecoration _decoration(AgentSchemaField f, {String? hint}) =>
      InputDecoration(
        labelText: f.label,
        hintText: hint,
        border: const OutlineInputBorder(),
        suffixIcon: f.description.isEmpty
            ? null
            : Icon(Icons.info_outline, size: 16, color: Colors.grey.shade500),
      );

  Widget _input(BuildContext context, AgentSchemaField f) {
    switch (f.type) {
      case AgentFieldType.select when !f.allowCustom:
        return _withTooltip(f, _select(f));
      case AgentFieldType.number:
        return _withTooltip(
          f,
          _NumberField(
            key: _key(f),
            field: f,
            value: valueOf(f.key) as num?,
            decoration: _decoration(f, hint: _defaultHint(f)),
            enabled: enabled,
            onChanged: (v) => onChanged(f.key, v),
          ),
        );
      default:
        // Text, and selects whose options are only suggestions.
        final value = (valueOf(f.key) as String?) ?? '';
        final suggestions = f.options.isEmpty
            ? null
            : 'Suggestions: ${f.options.join(', ')}';
        return _withTooltip(
          f,
          TextFormField(
            key: _key(f),
            initialValue: value,
            enabled: enabled,
            decoration: _decoration(
              f,
              hint:
                  _defaultHint(f) ??
                  (f.emptyLabel.isEmpty ? null : f.emptyLabel),
            ).copyWith(helperText: suggestions),
            onChanged: (v) =>
                onChanged(f.key, v.trim().isEmpty ? null : v.trim()),
          ),
        );
    }
  }

  String? _defaultHint(AgentSchemaField f) {
    final d = f.defaultValue;
    if (d == null || d == '' || d == 0) return null;
    return 'default: $d';
  }

  Widget _select(AgentSchemaField f) {
    final raw = valueOf(f.key) as String?;
    final value = raw == null || raw.isEmpty ? null : raw;
    final unlisted = value != null && !f.options.contains(value) ? value : null;
    final empty = f.emptyLabel.isNotEmpty
        ? f.emptyLabel
        : f.defaultValue is String && (f.defaultValue as String).isNotEmpty
        ? 'Default (${f.defaultValue})'
        : defaultEmptyLabel;
    return DropdownButtonFormField<String>(
      key: _key(f),
      // ignore: deprecated_member_use
      value: value,
      decoration: _decoration(f),
      items: [
        DropdownMenuItem<String>(value: null, child: Text(empty)),
        if (unlisted != null)
          DropdownMenuItem(
            value: unlisted,
            child: Text('$unlisted (not listed)'),
          ),
        ...f.options.map((o) => DropdownMenuItem(value: o, child: Text(o))),
      ],
      onChanged: enabled ? (v) => onChanged(f.key, v) : null,
    );
  }

  Widget _toggle(AgentSchemaField f) {
    final value = (f.effective({f.key: valueOf(f.key)}) as bool?) ?? false;
    // A dangerous toggle can only be turned off here: turning it on is a
    // config.toml decision, and the daemon refuses it over HTTP.
    final canChange = enabled && (!f.warning || value);
    final tooltip = f.warning
        ? '${f.description.isEmpty ? '⚠️ Dangerous.' : f.description}'
              '${f.description.contains('config.toml') ? '' : '\nThis screen can only disable it; enable it in config.toml.'}'
        : f.description;
    final row = Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        Switch(
          key: _key(f),
          value: value,
          onChanged: canChange ? (v) => onChanged(f.key, v) : null,
          // ignore: deprecated_member_use
          activeColor: f.warning ? Colors.orange.shade700 : null,
        ),
        const SizedBox(width: 4),
        Text(
          f.label,
          style: TextStyle(
            fontSize: 12,
            fontFamily: 'monospace',
            color: f.warning
                ? (value ? Colors.orange.shade700 : Colors.grey.shade500)
                : null,
          ),
        ),
        if (tooltip.isNotEmpty) ...[
          const SizedBox(width: 4),
          Icon(Icons.info_outline, size: 13, color: Colors.grey.shade500),
        ],
      ],
    );
    return tooltip.isEmpty
        ? row
        : Tooltip(
            message: tooltip,
            waitDuration: const Duration(milliseconds: 600),
            child: row,
          );
  }
}

/// A number input that reports only values within the field's bounds; an
/// empty input means "not set".
class _NumberField extends StatefulWidget {
  final AgentSchemaField field;
  final num? value;
  final InputDecoration decoration;
  final bool enabled;
  final ValueChanged<num?> onChanged;

  const _NumberField({
    super.key,
    required this.field,
    required this.value,
    required this.decoration,
    required this.enabled,
    required this.onChanged,
  });

  @override
  State<_NumberField> createState() => _NumberFieldState();
}

class _NumberFieldState extends State<_NumberField> {
  String? _error;

  String? _check(String raw) {
    final f = widget.field;
    if (raw.trim().isEmpty) return null;
    final n = num.tryParse(raw.trim());
    if (n == null) return 'Not a number';
    if (n < 0) return 'Must not be negative';
    if (f.min != null && n < f.min!) return 'At least ${f.min}';
    if (f.max != null && n > f.max!) return 'At most ${f.max}';
    return null;
  }

  @override
  Widget build(BuildContext context) {
    final v = widget.value;
    return TextFormField(
      initialValue: v == null || v == 0 ? '' : '$v',
      enabled: widget.enabled,
      decoration: widget.decoration.copyWith(errorText: _error),
      keyboardType: const TextInputType.numberWithOptions(decimal: true),
      inputFormatters: [FilteringTextInputFormatter.allow(RegExp(r'[0-9.]'))],
      onChanged: (raw) {
        final error = _check(raw);
        setState(() => _error = error);
        if (error != null) return;
        widget.onChanged(raw.trim().isEmpty ? null : num.parse(raw.trim()));
      },
    );
  }
}
