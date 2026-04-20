import 'package:flutter/material.dart';

/// A field that shows the effective value (global or overridden) with
/// visual indicators and a reset-to-global control.
///
/// When [overrideValue] is null, the field shows [globalValue] in muted
/// style with a "global" badge. When non-null, it shows the override
/// with a green left border, "overridden" badge, and reset button.
class OverrideTextField extends StatefulWidget {
  final String label;
  final String? helper;
  final String globalValue;
  final String? overrideValue;
  final ValueChanged<String?> onChanged;

  const OverrideTextField({
    super.key,
    required this.label,
    this.helper,
    required this.globalValue,
    required this.overrideValue,
    required this.onChanged,
  });

  @override
  State<OverrideTextField> createState() => _OverrideTextFieldState();
}

class _OverrideTextFieldState extends State<OverrideTextField> {
  late TextEditingController _ctrl;
  bool _selfEditing = false;

  @override
  void initState() {
    super.initState();
    _ctrl = TextEditingController(text: widget.overrideValue ?? widget.globalValue);
  }

  @override
  void didUpdateWidget(OverrideTextField old) {
    super.didUpdateWidget(old);
    // Only update the controller from external changes (reset, config reload).
    // Skip when the change originated from our own onChanged to avoid
    // clobbering the user's in-progress typing.
    if (_selfEditing) {
      _selfEditing = false;
      return;
    }
    if (widget.overrideValue != old.overrideValue ||
        widget.globalValue != old.globalValue) {
      _ctrl.text = widget.overrideValue ?? widget.globalValue;
    }
  }

  @override
  void dispose() {
    _ctrl.dispose();
    super.dispose();
  }

  bool get _isOverridden => widget.overrideValue != null;

  void _reset() {
    _ctrl.text = widget.globalValue;
    widget.onChanged(null);
  }

  void _handleChange(String value) {
    _selfEditing = true;
    final trimmed = value.trim();
    if (trimmed.isEmpty || trimmed == widget.globalValue) {
      widget.onChanged(null);
    } else {
      widget.onChanged(trimmed);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Container(
      decoration: BoxDecoration(
        color: Theme.of(context).colorScheme.surfaceContainerHighest.withValues(alpha: 0.3),
        borderRadius: BorderRadius.circular(6),
        border: Border(
          left: BorderSide(
            width: 3,
            color: _isOverridden ? Colors.green.shade600 : Colors.transparent,
          ),
        ),
      ),
      padding: const EdgeInsets.all(10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Text(widget.label,
                  style: TextStyle(fontSize: 11, color: Colors.grey.shade500)),
              const Spacer(),
              if (_isOverridden) ...[
                Container(
                  padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
                  decoration: BoxDecoration(
                    color: Colors.green.shade900.withValues(alpha: 0.4),
                    borderRadius: BorderRadius.circular(3),
                  ),
                  child: Text('overridden',
                      style: TextStyle(fontSize: 10, color: Colors.green.shade400)),
                ),
                const SizedBox(width: 6),
                GestureDetector(
                  onTap: _reset,
                  child: Container(
                    padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
                    decoration: BoxDecoration(
                      border: Border.all(color: Colors.grey.shade700),
                      borderRadius: BorderRadius.circular(3),
                    ),
                    child: Text('\u00d7 reset',
                        style: TextStyle(fontSize: 10, color: Colors.grey.shade500)),
                  ),
                ),
              ] else
                Text('global',
                    style: TextStyle(fontSize: 10, color: Colors.grey.shade600)),
            ],
          ),
          const SizedBox(height: 6),
          TextFormField(
            controller: _ctrl,
            style: TextStyle(
              fontSize: 12,
              color: _isOverridden ? null : Colors.grey.shade600,
            ),
            decoration: InputDecoration(
              isDense: true,
              border: const OutlineInputBorder(),
              helperText: widget.helper,
              helperMaxLines: 2,
              contentPadding: const EdgeInsets.symmetric(horizontal: 8, vertical: 8),
            ),
            onChanged: _handleChange,
          ),
          if (_isOverridden)
            Padding(
              padding: const EdgeInsets.only(top: 4),
              child: Text('Global: ${widget.globalValue}',
                  style: TextStyle(fontSize: 10, color: Colors.grey.shade700)),
            ),
        ],
      ),
    );
  }
}

/// Dropdown variant of the override field.
class OverrideDropdown extends StatelessWidget {
  final String label;
  final String globalValue;
  final String? overrideValue;
  final List<String> options;
  final ValueChanged<String?> onChanged;

  const OverrideDropdown({
    super.key,
    required this.label,
    required this.globalValue,
    required this.overrideValue,
    required this.options,
    required this.onChanged,
  });

  bool get _isOverridden => overrideValue != null;

  @override
  Widget build(BuildContext context) {
    return Container(
      decoration: BoxDecoration(
        color: Theme.of(context).colorScheme.surfaceContainerHighest.withValues(alpha: 0.3),
        borderRadius: BorderRadius.circular(6),
        border: Border(
          left: BorderSide(
            width: 3,
            color: _isOverridden ? Colors.green.shade600 : Colors.transparent,
          ),
        ),
      ),
      padding: const EdgeInsets.all(10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Text(label,
                  style: TextStyle(fontSize: 11, color: Colors.grey.shade500)),
              const Spacer(),
              if (_isOverridden) ...[
                Container(
                  padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
                  decoration: BoxDecoration(
                    color: Colors.green.shade900.withValues(alpha: 0.4),
                    borderRadius: BorderRadius.circular(3),
                  ),
                  child: Text('overridden',
                      style: TextStyle(fontSize: 10, color: Colors.green.shade400)),
                ),
                const SizedBox(width: 6),
                GestureDetector(
                  onTap: () => onChanged(null),
                  child: Container(
                    padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
                    decoration: BoxDecoration(
                      border: Border.all(color: Colors.grey.shade700),
                      borderRadius: BorderRadius.circular(3),
                    ),
                    child: Text('\u00d7 reset',
                        style: TextStyle(fontSize: 10, color: Colors.grey.shade500)),
                  ),
                ),
              ] else
                Text('global',
                    style: TextStyle(fontSize: 10, color: Colors.grey.shade600)),
            ],
          ),
          const SizedBox(height: 6),
          DropdownButtonFormField<String?>(
            initialValue: overrideValue,
            decoration: const InputDecoration(
              isDense: true,
              border: OutlineInputBorder(),
              contentPadding: EdgeInsets.symmetric(horizontal: 8, vertical: 8),
            ),
            items: [
              DropdownMenuItem<String?>(
                value: null,
                child: Text('Global ($globalValue)',
                    style: TextStyle(color: Colors.grey.shade600, fontSize: 12)),
              ),
              ...options.map((v) => DropdownMenuItem<String?>(
                    value: v,
                    child: Text(v, style: const TextStyle(fontSize: 12)),
                  )),
            ],
            onChanged: onChanged,
          ),
        ],
      ),
    );
  }
}
