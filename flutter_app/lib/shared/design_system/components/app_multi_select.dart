import 'package:flutter/material.dart';

import '../tokens.dart';
import 'app_text.dart';

/// A themed pill that shows the current selection count and opens
/// [showAppMultiSelect] on tap.
///
/// Replaces the toolbar-side "Org"/"Repo"-style chips that used to each
/// carry their own `GestureDetector` + `showDialog` call.
class AppMultiSelectChip extends StatelessWidget {
  final String label;
  final IconData icon;
  final List<String> allValues;
  final Set<String> selected;
  final String Function(String)? displayFn;
  final ValueChanged<Set<String>> onChanged;

  const AppMultiSelectChip({
    super.key,
    required this.label,
    required this.icon,
    required this.allValues,
    required this.selected,
    required this.onChanged,
    this.displayFn,
  });

  @override
  Widget build(BuildContext context) {
    final hasSelection = selected.isNotEmpty;
    final accent = AppColors.accent.resolve(context);
    final muted = AppColors.textMuted.resolve(context);
    final color = hasSelection ? accent : muted;
    final pillRadius = BorderRadius.all(AppRadius.pill.resolve(context));

    return InkWell(
      borderRadius: pillRadius,
      onTap: () async {
        final result = await showAppMultiSelect(
          context: context,
          title: label,
          allValues: allValues,
          selected: selected,
          displayFn: displayFn,
        );
        if (result != null) onChanged(result);
      },
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 6),
        decoration: BoxDecoration(
          borderRadius: pillRadius,
          border: Border.all(
            color: color.withValues(alpha: hasSelection ? 0.5 : 0.4),
          ),
          color: hasSelection
              ? accent.withValues(alpha: 0.1)
              : Colors.transparent,
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(icon, size: 14, color: color),
            const SizedBox(width: 4),
            AppText.label(
              hasSelection ? '$label (${selected.length})' : label,
              color: color,
            ),
          ],
        ),
      ),
    );
  }
}

/// Shows the single, shared multi-select dialog used across every toolbar
/// that needs an org/repo/type filter.
///
/// Replaces four near-identical implementations that had drifted apart:
/// two private classes both named `_MultiSelectDialog`
/// (`repos_screen.dart:901`, `stats_filter_bar.dart:130`), the inline
/// `showDialog` in `activity_filter_bar.dart:337`, and the
/// `showModalBottomSheet` in `activity_filter_chips.dart`.
Future<Set<String>?> showAppMultiSelect({
  required BuildContext context,
  required String title,
  required List<String> allValues,
  required Set<String> selected,
  String Function(String)? displayFn,
}) {
  final display = displayFn ?? (String v) => v;
  var current = Set<String>.from(selected);

  return showDialog<Set<String>>(
    context: context,
    builder: (dialogContext) => StatefulBuilder(
      builder: (dialogContext, setDialogState) {
        final accent = AppColors.accent.resolve(dialogContext);
        final muted = AppColors.textMuted.resolve(dialogContext);
        final border = AppColors.border.resolve(dialogContext);

        return Dialog(
          shape: RoundedRectangleBorder(
            borderRadius: BorderRadius.circular(12),
            side: BorderSide(color: border),
          ),
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 340, maxHeight: 420),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                Padding(
                  padding: const EdgeInsets.fromLTRB(20, 16, 12, 8),
                  child: Row(
                    children: [
                      Expanded(child: AppText.sectionTitle('Filter by $title')),
                      if (current.isNotEmpty)
                        TextButton(
                          onPressed: () => setDialogState(() => current = {}),
                          child: const Text('Clear'),
                        ),
                    ],
                  ),
                ),
                const Divider(height: 1),
                Flexible(
                  child: ListView.builder(
                    shrinkWrap: true,
                    padding: const EdgeInsets.symmetric(vertical: 4),
                    itemCount: allValues.length,
                    itemBuilder: (_, i) {
                      final v = allValues[i];
                      final checked = current.contains(v);
                      return InkWell(
                        onTap: () => setDialogState(() {
                          checked ? current.remove(v) : current.add(v);
                        }),
                        child: Padding(
                          padding: const EdgeInsets.symmetric(
                            horizontal: 20,
                            vertical: 10,
                          ),
                          child: Row(
                            children: [
                              Expanded(
                                child: AppText(
                                  display(v),
                                  color: checked ? accent : null,
                                ),
                              ),
                              Icon(
                                checked
                                    ? Icons.check_box
                                    : Icons.check_box_outline_blank,
                                size: 20,
                                color: checked ? accent : muted,
                              ),
                            ],
                          ),
                        ),
                      );
                    },
                  ),
                ),
                const Divider(height: 1),
                Padding(
                  padding: const EdgeInsets.all(12),
                  child: SizedBox(
                    width: double.infinity,
                    child: FilledButton(
                      onPressed: () => Navigator.of(dialogContext).pop(current),
                      child: Text(
                        'Apply${current.isNotEmpty ? ' (${current.length})' : ''}',
                      ),
                    ),
                  ),
                ),
              ],
            ),
          ),
        );
      },
    ),
  );
}
