import 'package:flutter/material.dart';

import '../../core/models/config_model.dart';
import '../../core/models/github_principal.dart';

/// Edits a list of GitHub logins and org teams ("org/team") as chips, with
/// autocomplete from [suggestions]. Suggestions only help: any typed entry is
/// accepted, so the field works without them (older daemon, no read:org).
///
/// Typed text becomes chips on Enter, on a comma or space (a pasted
/// "a, b" splits), on picking a suggestion, or when the field loses focus.
class PrincipalChipsField extends StatefulWidget {
  final List<String> value;
  final ValueChanged<List<String>> onChanged;
  final List<GitHubPrincipal> suggestions;
  final String? label;
  final String? helper;

  /// Renders the chips greyed out, for an inherited (not overridden) value.
  final bool muted;

  const PrincipalChipsField({
    super.key,
    required this.value,
    required this.onChanged,
    this.suggestions = const [],
    this.label,
    this.helper,
    this.muted = false,
  });

  @override
  State<PrincipalChipsField> createState() => _PrincipalChipsFieldState();
}

class _PrincipalChipsFieldState extends State<PrincipalChipsField> {
  final _ctrl = TextEditingController();
  final _focus = FocusNode();

  @override
  void initState() {
    super.initState();
    _focus.addListener(() {
      if (!_focus.hasFocus) _commit(_ctrl.text);
    });
  }

  @override
  void dispose() {
    _ctrl.dispose();
    _focus.dispose();
    super.dispose();
  }

  bool _has(String entry) =>
      widget.value.any((v) => v.toLowerCase() == entry.toLowerCase());

  void _commit(String text) {
    final added = <String>[];
    for (final e in parseLoginList(text)) {
      if (!_has(e) && !added.any((a) => a.toLowerCase() == e.toLowerCase())) {
        added.add(e);
      }
    }
    _ctrl.clear();
    if (added.isNotEmpty) widget.onChanged([...widget.value, ...added]);
  }

  Iterable<GitHubPrincipal> _matches(String text) {
    final q = text.trim().toLowerCase().replaceFirst('@', '');
    if (q.isEmpty) return const [];
    return widget.suggestions
        .where((p) => p.value.toLowerCase().contains(q) && !_has(p.value))
        .take(8);
  }

  @override
  Widget build(BuildContext context) {
    final muted = widget.muted ? Colors.grey.shade600 : null;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      mainAxisSize: MainAxisSize.min,
      children: [
        if (widget.value.isNotEmpty) ...[
          Wrap(
            spacing: 6,
            runSpacing: 6,
            children: [
              for (final v in widget.value)
                InputChip(
                  avatar: Icon(
                    v.contains('/') ? Icons.group : Icons.person,
                    size: 16,
                  ),
                  label: Text(v, style: TextStyle(fontSize: 12, color: muted)),
                  visualDensity: VisualDensity.compact,
                  deleteButtonTooltipMessage: 'Remove $v',
                  onDeleted: () => widget.onChanged(
                    widget.value.where((e) => e != v).toList(),
                  ),
                ),
            ],
          ),
          const SizedBox(height: 8),
        ],
        LayoutBuilder(
          builder: (context, constraints) => RawAutocomplete<GitHubPrincipal>(
            textEditingController: _ctrl,
            focusNode: _focus,
            optionsBuilder: (value) => _matches(value.text),
            displayStringForOption: (p) => p.value,
            onSelected: (p) => _commit(p.value),
            fieldViewBuilder: (context, ctrl, focus, onFieldSubmitted) =>
                TextField(
                  controller: ctrl,
                  focusNode: focus,
                  autocorrect: false,
                  enableSuggestions: false,
                  style: const TextStyle(fontSize: 12),
                  decoration: InputDecoration(
                    labelText: widget.label,
                    hintText: 'Add a login or org/team',
                    helperText: widget.helper,
                    helperMaxLines: 2,
                    isDense: true,
                    border: const OutlineInputBorder(),
                  ),
                  onChanged: (v) {
                    if (v.contains(RegExp(r'[,\s]'))) _commit(v);
                  },
                  // Keep focus on Enter so several entries go in a row.
                  onEditingComplete: () {},
                  onSubmitted: (v) {
                    // Enter picks the highlighted suggestion unless the text
                    // is already a complete entry nobody suggested.
                    if (_matches(v).isEmpty) {
                      _commit(v);
                    } else {
                      onFieldSubmitted();
                    }
                  },
                ),
            optionsViewBuilder: (context, onSelected, options) {
              final list = options.toList(growable: false);
              return Align(
                alignment: Alignment.topLeft,
                child: Material(
                  elevation: 4,
                  borderRadius: BorderRadius.circular(8),
                  child: ConstrainedBox(
                    constraints: BoxConstraints(
                      maxHeight: 280,
                      maxWidth: constraints.maxWidth,
                    ),
                    child: ListView.builder(
                      padding: EdgeInsets.zero,
                      shrinkWrap: true,
                      itemCount: list.length,
                      itemBuilder: (context, i) {
                        final p = list[i];
                        return ListTile(
                          dense: true,
                          selected:
                              AutocompleteHighlightedOption.of(context) == i,
                          leading: Icon(
                            p.isTeam ? Icons.group : Icons.person,
                            size: 18,
                          ),
                          title: Text(p.value),
                          onTap: () => onSelected(p),
                        );
                      },
                    ),
                  ),
                ),
              );
            },
          ),
        ),
      ],
    );
  }
}
