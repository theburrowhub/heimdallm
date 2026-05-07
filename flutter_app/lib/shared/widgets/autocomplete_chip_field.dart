import 'package:flutter/material.dart';

/// A chip-based input field with autocomplete suggestions.
/// Shows selected values as chips with remove buttons, and a text field
/// that filters suggestions from [availableOptions] as the user types.
class AutocompleteChipField extends StatefulWidget {
  final String label;
  final String? helper;
  final List<String> selectedValues;
  final List<String> availableOptions;
  final String? globalHint;
  final String inheritedLabel;
  final bool isOverridden;
  final ValueChanged<List<String>?> onChanged;
  final VoidCallback? onReset;

  const AutocompleteChipField({
    super.key,
    required this.label,
    this.helper,
    required this.selectedValues,
    required this.availableOptions,
    this.globalHint,
    this.inheritedLabel = 'global',
    this.isOverridden = false,
    required this.onChanged,
    this.onReset,
  });

  @override
  State<AutocompleteChipField> createState() => _AutocompleteChipFieldState();
}

class _AutocompleteChipFieldState extends State<AutocompleteChipField> {
  final _ctrl = TextEditingController();
  final _focusNode = FocusNode();
  bool _showSuggestions = false;

  List<String> get _filteredSuggestions {
    final query = _ctrl.text.trim().toLowerCase();
    final alreadySelected = widget.selectedValues.toSet();
    return widget.availableOptions
        .where((o) => !alreadySelected.contains(o))
        .where((o) => query.isEmpty || o.toLowerCase().contains(query))
        .take(8)
        .toList();
  }

  void _addValue(String value) {
    final updated = [...widget.selectedValues, value];
    widget.onChanged(updated);
    _ctrl.clear();
    setState(() => _showSuggestions = false);
    _focusNode.requestFocus();
  }

  void _removeValue(String value) {
    final updated = widget.selectedValues.where((v) => v != value).toList();
    widget.onChanged(updated);
  }

  void _handleSubmit(String text) {
    final trimmed = text.trim();
    if (trimmed.isNotEmpty && !widget.selectedValues.contains(trimmed)) {
      _addValue(trimmed);
    }
  }

  @override
  void dispose() {
    _ctrl.dispose();
    _focusNode.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Container(
      decoration: BoxDecoration(
        color: Theme.of(
          context,
        ).colorScheme.surfaceContainerHighest.withValues(alpha: 0.3),
        borderRadius: BorderRadius.circular(6),
        border: Border(
          left: BorderSide(
            width: 3,
            color: widget.isOverridden
                ? Colors.green.shade600
                : Colors.transparent,
          ),
        ),
      ),
      padding: const EdgeInsets.all(10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          // Header with label + override badge
          Row(
            children: [
              Text(
                widget.label,
                style: TextStyle(fontSize: 11, color: Colors.grey.shade500),
              ),
              const Spacer(),
              if (widget.isOverridden) ...[
                Container(
                  padding: const EdgeInsets.symmetric(
                    horizontal: 6,
                    vertical: 1,
                  ),
                  decoration: BoxDecoration(
                    color: Colors.green.shade900.withValues(alpha: 0.4),
                    borderRadius: BorderRadius.circular(3),
                  ),
                  child: Text(
                    'overridden',
                    style: TextStyle(
                      fontSize: 10,
                      color: Colors.green.shade400,
                    ),
                  ),
                ),
                if (widget.onReset != null) ...[
                  const SizedBox(width: 6),
                  GestureDetector(
                    onTap: widget.onReset,
                    child: Container(
                      padding: const EdgeInsets.symmetric(
                        horizontal: 6,
                        vertical: 1,
                      ),
                      decoration: BoxDecoration(
                        border: Border.all(color: Colors.grey.shade700),
                        borderRadius: BorderRadius.circular(3),
                      ),
                      child: Text(
                        '\u00d7 reset',
                        style: TextStyle(
                          fontSize: 10,
                          color: Colors.grey.shade500,
                        ),
                      ),
                    ),
                  ),
                ],
              ] else
                Text(
                  widget.inheritedLabel,
                  style: TextStyle(fontSize: 10, color: Colors.grey.shade600),
                ),
            ],
          ),
          const SizedBox(height: 6),
          // Chips
          if (widget.selectedValues.isNotEmpty)
            Padding(
              padding: const EdgeInsets.only(bottom: 6),
              child: Wrap(
                spacing: 4,
                runSpacing: 4,
                children: widget.selectedValues
                    .map(
                      (v) => Chip(
                        label: Text(v, style: const TextStyle(fontSize: 11)),
                        deleteIcon: const Icon(Icons.close, size: 14),
                        onDeleted: () => _removeValue(v),
                        materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
                        visualDensity: VisualDensity.compact,
                      ),
                    )
                    .toList(),
              ),
            ),
          // Text input + suggestions
          Focus(
            onFocusChange: (focused) {
              if (!focused) {
                // Delay hiding so tap on suggestion registers first
                Future.delayed(const Duration(milliseconds: 200), () {
                  if (mounted) setState(() => _showSuggestions = false);
                });
              }
            },
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                TextFormField(
                  controller: _ctrl,
                  focusNode: _focusNode,
                  style: const TextStyle(fontSize: 12),
                  decoration: InputDecoration(
                    isDense: true,
                    border: const OutlineInputBorder(),
                    hintText: 'Type to add...',
                    helperText: widget.helper,
                    helperMaxLines: 2,
                    contentPadding: const EdgeInsets.symmetric(
                      horizontal: 8,
                      vertical: 8,
                    ),
                  ),
                  onChanged: (_) => setState(() => _showSuggestions = true),
                  onFieldSubmitted: _handleSubmit,
                ),
                if (_showSuggestions && _filteredSuggestions.isNotEmpty)
                  Container(
                    margin: const EdgeInsets.only(top: 2),
                    constraints: const BoxConstraints(maxHeight: 160),
                    decoration: BoxDecoration(
                      color: Theme.of(
                        context,
                      ).colorScheme.surfaceContainerHighest,
                      borderRadius: BorderRadius.circular(4),
                      border: Border.all(color: Colors.grey.shade700),
                    ),
                    child: ListView(
                      shrinkWrap: true,
                      padding: EdgeInsets.zero,
                      children: _filteredSuggestions
                          .map(
                            (s) => InkWell(
                              onTap: () => _addValue(s),
                              child: Padding(
                                padding: const EdgeInsets.symmetric(
                                  horizontal: 10,
                                  vertical: 6,
                                ),
                                child: Text(
                                  s,
                                  style: const TextStyle(fontSize: 12),
                                ),
                              ),
                            ),
                          )
                          .toList(),
                    ),
                  ),
              ],
            ),
          ),
          if (widget.isOverridden && widget.globalHint != null)
            Padding(
              padding: const EdgeInsets.only(top: 4),
              child: Text(
                '${widget.inheritedLabel}: ${widget.globalHint}',
                style: TextStyle(fontSize: 10, color: Colors.grey.shade700),
              ),
            ),
        ],
      ),
    );
  }
}
