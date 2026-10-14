import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/shared/design_system/color_resolver.dart';
import 'package:heimdallm/shared/design_system/components/components.dart';
import 'package:heimdallm/shared/design_system/tokens.dart';

/// The inspector's heading: an accent icon, a title, the step id and a
/// one-line explanation.
class InspectorHeader extends StatelessWidget {
  final IconData icon;
  final Color accent;
  final String title;
  final String? id;
  final String description;

  const InspectorHeader({
    super.key,
    required this.icon,
    required this.accent,
    required this.title,
    this.id,
    required this.description,
  });

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Row(
          children: [
            Container(
              width: 28,
              height: 28,
              decoration: BoxDecoration(
                color: accent.withValues(alpha: .14),
                borderRadius: BorderRadius.circular(8),
              ),
              child: Icon(icon, size: 16, color: accent),
            ),
            const SizedBox(width: 10),
            Expanded(child: AppText.sectionTitle(title)),
            if (id != null) AppText.mono(id!),
          ],
        ),
        const SizedBox(height: 8),
        AppText.muted(description),
      ],
    );
  }
}

/// An uppercase section label with optional spacing above it.
class InspectorLabel extends StatelessWidget {
  final String text;
  const InspectorLabel(this.text, {super.key});

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.only(top: 20, bottom: 6),
    child: Text(
      text.toUpperCase(),
      style: Theme.of(context).textTheme.labelSmall?.copyWith(
        color: resolveAppColor(context, AppColors.textMuted),
        letterSpacing: .8,
        fontWeight: FontWeight.w600,
      ),
    ),
  );
}

/// Tells the operator why a step's fields are locked on the built-in default
/// flow, and where its agents are changed instead.
class InspectorReadOnlyNote extends StatelessWidget {
  const InspectorReadOnlyNote({super.key});

  @override
  Widget build(BuildContext context) => const Padding(
    padding: EdgeInsets.only(top: 12),
    child: AppText.muted(
      'The default flow follows the global primary and fallback — change '
      'them in the flow panel.',
    ),
  );
}

/// Problems with the selected step, from the client check or the daemon.
class InspectorIssues extends StatelessWidget {
  final List<FlowIssue> issues;
  const InspectorIssues({super.key, required this.issues});

  @override
  Widget build(BuildContext context) {
    if (issues.isEmpty) return const SizedBox.shrink();
    final danger = resolveAppColor(context, AppColors.danger);
    return Padding(
      padding: const EdgeInsets.only(top: 16),
      child: Container(
        padding: const EdgeInsets.all(10),
        decoration: BoxDecoration(
          color: danger.withValues(alpha: .08),
          border: Border.all(color: danger.withValues(alpha: .35)),
          borderRadius: BorderRadius.circular(8),
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            for (final i in issues)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 2),
                child: Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Icon(Icons.error_outline, size: 16, color: danger),
                    const SizedBox(width: 8),
                    Expanded(
                      child: Text(
                        i.message,
                        style: Theme.of(
                          context,
                        ).textTheme.bodySmall?.copyWith(color: danger),
                      ),
                    ),
                  ],
                ),
              ),
          ],
        ),
      ),
    );
  }
}

/// A text field bound to a value held elsewhere: typing reports through
/// [onChanged], and an outside change of [value] (undo, redo) replaces the
/// text unless [same] says the text already means that value (so "49."
/// stays while the operator is typing "49.5").
class SyncedTextField extends StatefulWidget {
  final String value;
  final ValueChanged<String> onChanged;
  final bool Function(String text, String value)? same;
  final String label;
  final String? hint;
  final List<TextInputFormatter>? inputFormatters;
  final int? maxLength;
  final bool enabled;
  final TextInputType? keyboardType;

  const SyncedTextField({
    super.key,
    required this.value,
    required this.onChanged,
    required this.label,
    this.same,
    this.hint,
    this.inputFormatters,
    this.maxLength,
    this.enabled = true,
    this.keyboardType,
  });

  @override
  State<SyncedTextField> createState() => _SyncedTextFieldState();
}

class _SyncedTextFieldState extends State<SyncedTextField> {
  late final _ctl = TextEditingController(text: widget.value);

  @override
  void didUpdateWidget(SyncedTextField old) {
    super.didUpdateWidget(old);
    final same = widget.same ?? (a, b) => a == b;
    if (widget.value != old.value && !same(_ctl.text, widget.value)) {
      _ctl.text = widget.value;
    }
  }

  @override
  void dispose() {
    _ctl.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => TextField(
    controller: _ctl,
    enabled: widget.enabled,
    maxLength: widget.maxLength,
    keyboardType: widget.keyboardType,
    inputFormatters: widget.inputFormatters,
    style: Theme.of(context).textTheme.bodyMedium,
    decoration: InputDecoration(
      labelText: widget.label,
      hintText: widget.hint,
      isDense: true,
      border: const OutlineInputBorder(),
    ),
    onChanged: widget.onChanged,
  );
}
