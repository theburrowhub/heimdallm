import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/models/agent_schema.dart';
import '../../shared/design_system/components/components.dart';

/// Renders an agent's settings form from its schema: dropdowns for select
/// fields, switches for toggles, and text inputs for numbers and text. The
/// form holds no state of its own: [valueOf] reads the current value and
/// [onChanged] reports an edit (null means "not set").
///
/// Values come from config.toml, which can be edited by hand, so they are
/// read leniently: a value of the wrong type shows as "not set" (or as its
/// text) instead of breaking the page.
class AgentSchemaForm extends StatelessWidget {
  final List<AgentSchemaField> fields;
  final Object? Function(String key) valueOf;
  final void Function(String key, Object? value) onChanged;

  /// Reports whether a field's typed input is invalid (a number out of
  /// range). While one is, the caller must not save: the field shows the
  /// typed text, which is not the value it last reported.
  final void Function(String key, bool invalid)? onInvalidChanged;

  /// Distinguishes the widgets of different agents (`agent-field-<prefix>-<key>`).
  final String keyPrefix;

  /// Label of the empty choice when a field names none.
  final String defaultEmptyLabel;

  /// Read-only preview (the custom agent editor).
  final bool enabled;

  /// Numbers must be whole: a built-in agent's numeric settings are integers.
  final bool integerNumbers;

  const AgentSchemaForm({
    super.key,
    required this.fields,
    required this.valueOf,
    required this.onChanged,
    required this.keyPrefix,
    this.onInvalidChanged,
    this.defaultEmptyLabel = 'CLI default',
    this.enabled = true,
    this.integerNumbers = false,
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
            value: asSettingNum(valueOf(f.key)),
            decoration: _decoration(f, hint: _defaultHint(f)),
            enabled: enabled,
            integer: integerNumbers,
            onChanged: (v) => onChanged(f.key, v),
            onInvalidChanged: (invalid) =>
                onInvalidChanged?.call(f.key, invalid),
          ),
        );
      default:
        // Text, and selects whose options are only suggestions.
        final suggestions = f.options.isEmpty
            ? null
            : 'Suggestions: ${f.options.join(', ')}';
        return _withTooltip(
          f,
          _SyncedTextField(
            key: _key(f),
            text: asSettingString(valueOf(f.key)) ?? '',
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
    final raw = asSettingString(valueOf(f.key));
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
    final value =
        asSettingBool(valueOf(f.key)) ?? asSettingBool(f.defaultValue) ?? false;
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

/// A setting read as text: strings as they are, numbers and booleans as
/// their text, anything else as not set.
String? asSettingString(Object? v) => switch (v) {
  final String s => s,
  final num n => '$n',
  final bool b => '$b',
  _ => null,
};

/// A setting read as a number: numbers, and strings that parse as one.
num? asSettingNum(Object? v) => switch (v) {
  final num n => n,
  final String s => num.tryParse(s.trim()),
  _ => null,
};

/// A setting read as a toggle: booleans, and the strings "true"/"false".
bool? asSettingBool(Object? v) => switch (v) {
  final bool b => b,
  'true' => true,
  'false' => false,
  _ => null,
};

/// A text field that follows [text] when it changes from outside (a reload,
/// a discarded edit), except while the user is typing in it.
class _SyncedTextField extends StatefulWidget {
  final String text;
  final bool enabled;
  final InputDecoration decoration;
  final TextInputType? keyboardType;
  final List<TextInputFormatter>? inputFormatters;
  final ValueChanged<String> onChanged;

  const _SyncedTextField({
    super.key,
    required this.text,
    required this.enabled,
    required this.decoration,
    required this.onChanged,
    this.keyboardType,
    this.inputFormatters,
  });

  @override
  State<_SyncedTextField> createState() => _SyncedTextFieldState();
}

class _SyncedTextFieldState extends State<_SyncedTextField> {
  late final TextEditingController _controller = TextEditingController(
    text: widget.text,
  );
  final FocusNode _focus = FocusNode();

  @override
  void didUpdateWidget(covariant _SyncedTextField old) {
    super.didUpdateWidget(old);
    if (widget.text != old.text &&
        widget.text != _controller.text &&
        !_focus.hasFocus) {
      _controller.text = widget.text;
    }
  }

  @override
  void dispose() {
    _controller.dispose();
    _focus.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => TextField(
    controller: _controller,
    focusNode: _focus,
    enabled: widget.enabled,
    decoration: widget.decoration,
    keyboardType: widget.keyboardType,
    inputFormatters: widget.inputFormatters,
    onChanged: widget.onChanged,
  );
}

/// A number input. An empty input means "not set"; 0 shows as "0" unless 0
/// is also the default ("not set" for the built-in agents' turn caps). Input
/// out of the field's bounds is not reported as a value: the field reports
/// itself invalid instead, so the caller cannot save the last partial value
/// typed on the way (1, then 15, then 150 over a maximum of 100).
class _NumberField extends StatefulWidget {
  final AgentSchemaField field;
  final num? value;
  final InputDecoration decoration;
  final bool enabled;
  final bool integer;
  final ValueChanged<num?> onChanged;
  final ValueChanged<bool> onInvalidChanged;

  const _NumberField({
    super.key,
    required this.field,
    required this.value,
    required this.decoration,
    required this.enabled,
    required this.integer,
    required this.onChanged,
    required this.onInvalidChanged,
  });

  @override
  State<_NumberField> createState() => _NumberFieldState();
}

class _NumberFieldState extends State<_NumberField> {
  String? _error;

  String _display(num? v) {
    if (v == null) return '';
    final d = asSettingNum(widget.field.defaultValue) ?? 0;
    if (v == 0 && d == 0) return '';
    return v == v.roundToDouble() ? '${v.toInt()}' : '$v';
  }

  String? _check(String raw) {
    final f = widget.field;
    if (raw.trim().isEmpty) return null;
    final n = num.tryParse(raw.trim());
    if (n == null) return 'Not a number';
    if (widget.integer && n != n.roundToDouble()) return 'Whole numbers only';
    if (n < 0) return 'Must not be negative';
    if (f.min != null && n < f.min!) return 'At least ${f.min}';
    if (f.max != null && n > f.max!) return 'At most ${f.max}';
    return null;
  }

  @override
  Widget build(BuildContext context) {
    return _SyncedTextField(
      text: _display(widget.value),
      enabled: widget.enabled,
      decoration: widget.decoration.copyWith(errorText: _error),
      keyboardType: TextInputType.numberWithOptions(decimal: !widget.integer),
      inputFormatters: [
        FilteringTextInputFormatter.allow(
          RegExp(widget.integer ? r'[0-9]' : r'[0-9.]'),
        ),
      ],
      onChanged: (raw) {
        final error = _check(raw);
        if (error != _error) {
          setState(() => _error = error);
          widget.onInvalidChanged(error != null);
        }
        if (error != null) return;
        final n = raw.trim().isEmpty ? null : num.parse(raw.trim());
        widget.onChanged(
          n == null ? null : (n == n.roundToDouble() ? n.toInt() : n),
        );
      },
    );
  }
}
