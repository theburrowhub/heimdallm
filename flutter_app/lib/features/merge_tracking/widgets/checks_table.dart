import 'package:flutter/material.dart';

import '../../../core/models/merge_tracking.dart';
import 'check_visuals.dart';

/// The per-check breakdown for one PR.
///
/// Designed around one idea: a grid of coloured dots is not an explanation. The
/// panel opens with a sentence in plain language, then lists the checks that
/// gate the merge, then hides the optional ones behind a disclosure so their
/// noise cannot obscure what is actually blocking.
class ChecksTable extends StatefulWidget {
  final MergeDecision decision;

  /// Called with a check's log URL when the user taps it. Left injectable so
  /// the widget stays free of url_launcher (and therefore of dart:io) and so
  /// tests can observe the tap.
  final void Function(String url)? onOpenUrl;

  const ChecksTable({super.key, required this.decision, this.onOpenUrl});

  @override
  State<ChecksTable> createState() => _ChecksTableState();
}

class _ChecksTableState extends State<ChecksTable> {
  bool _showOptional = false;

  @override
  Widget build(BuildContext context) {
    final decision = widget.decision;
    final required = decision.requiredChecks;
    final optional = decision.optionalChecks;

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _Headline(decision: decision),
        const SizedBox(height: 12),
        if (decision.checksSummary?.missingRequired.isNotEmpty ?? false)
          _MissingRequired(names: decision.checksSummary!.missingRequired),
        if (required.isEmpty && optional.isEmpty)
          Text(
            'No checks reported for this commit.',
            style: Theme.of(context).textTheme.bodySmall,
          )
        else ...[
          for (final check in required)
            _CheckRow(check: check, onOpenUrl: widget.onOpenUrl),
          if (optional.isNotEmpty) ...[
            const SizedBox(height: 4),
            InkWell(
              onTap: () => setState(() => _showOptional = !_showOptional),
              child: Padding(
                padding: const EdgeInsets.symmetric(vertical: 6),
                child: Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    Icon(
                      _showOptional ? Icons.expand_less : Icons.expand_more,
                      size: 18,
                    ),
                    const SizedBox(width: 4),
                    // Flexible: the panel also renders in the PR detail's
                    // narrow column, where this sentence does not fit on one
                    // line and an unconstrained Text overflows the row.
                    Flexible(
                      child: Text(
                        '${optional.length} optional '
                        '${optional.length == 1 ? 'check' : 'checks'} '
                        '(do not block the merge)',
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                    ),
                  ],
                ),
              ),
            ),
            if (_showOptional)
              for (final check in optional)
                _CheckRow(
                  check: check,
                  onOpenUrl: widget.onOpenUrl,
                  dimmed: true,
                ),
          ],
        ],
      ],
    );
  }
}

/// The sentence above the table. Derived from the counts rather than written
/// per state, so it cannot drift from what the rows show.
class _Headline extends StatelessWidget {
  final MergeDecision decision;

  const _Headline({required this.decision});

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final s = decision.checksSummary ?? const MergeChecksSummary();

    String text;
    Color color = scheme.onSurface;
    IconData icon = Icons.check_circle;

    if (s.truncated) {
      text =
          'This PR has more checks than Heimdallm can read in one pass, so its '
          'merge state cannot be confirmed.';
      color = scheme.error;
      icon = Icons.help;
    } else if (s.missingRequired.isNotEmpty) {
      final n = s.missingRequired.length;
      text =
          'Waiting for $n required ${n == 1 ? 'check' : 'checks'} that '
          '${n == 1 ? 'has' : 'have'} not run yet.';
      color = const Color(0xFFB26A00);
      icon = Icons.hourglass_top;
    } else if (s.requiredFailing > 0) {
      text =
          'This PR cannot be merged: ${s.requiredFailing} of the '
          '${s.requiredTotal} required checks '
          '${s.requiredFailing == 1 ? 'is' : 'are'} failing.';
      color = scheme.error;
      icon = Icons.error;
    } else if (s.requiredPending > 0) {
      text =
          'Waiting on ${s.requiredPending} required '
          '${s.requiredPending == 1 ? 'check' : 'checks'}. '
          'The PR merges on its own once they pass.';
      color = const Color(0xFFB26A00);
      icon = Icons.hourglass_top;
    } else if (s.optionalFailing > 0 && s.requiredTotal > 0) {
      text =
          'All ${s.requiredTotal} required checks passed. '
          '${s.optionalFailing} optional '
          '${s.optionalFailing == 1 ? 'check is' : 'checks are'} failing, '
          'which does not block the merge.';
      color = const Color(0xFF2E7D32);
    } else if (s.total == 0) {
      text = 'This PR has no checks configured.';
      color = scheme.onSurfaceVariant;
      icon = Icons.info_outline;
    } else {
      text = 'All ${s.total} checks passed.';
      color = const Color(0xFF2E7D32);
    }

    return Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Icon(icon, size: 20, color: color),
        const SizedBox(width: 8),
        Expanded(
          child: Text(
            text,
            style: Theme.of(context).textTheme.bodyMedium?.copyWith(
              color: color,
              fontWeight: FontWeight.w600,
            ),
          ),
        ),
      ],
    );
  }
}

/// Required contexts that never reported. Called out separately because
/// nothing red appears anywhere in GitHub's own UI for these.
class _MissingRequired extends StatelessWidget {
  final List<String> names;

  const _MissingRequired({required this.names});

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: Container(
        width: double.infinity,
        padding: const EdgeInsets.all(8),
        decoration: BoxDecoration(
          color: const Color(0xFFFFF3CD),
          borderRadius: BorderRadius.circular(6),
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text(
              'Required checks that have not reported',
              style: TextStyle(
                fontSize: 12,
                fontWeight: FontWeight.w700,
                color: Color(0xFF6B4300),
              ),
            ),
            const SizedBox(height: 4),
            for (final name in names)
              Text(
                '• $name',
                style: const TextStyle(
                  fontSize: 12,
                  color: Color(0xFF6B4300),
                  fontFamily: 'monospace',
                ),
              ),
          ],
        ),
      ),
    );
  }
}

class _CheckRow extends StatelessWidget {
  final MergeCheck check;
  final void Function(String url)? onOpenUrl;
  final bool dimmed;

  const _CheckRow({required this.check, this.onOpenUrl, this.dimmed = false});

  @override
  Widget build(BuildContext context) {
    final visuals = CheckVisuals.forCheck(context, check);
    final theme = Theme.of(context);
    final hasUrl = check.url.isNotEmpty && onOpenUrl != null;

    return Opacity(
      opacity: dimmed ? 0.65 : 1.0,
      child: Padding(
        padding: const EdgeInsets.symmetric(vertical: 5),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.center,
          children: [
            Tooltip(
              message: visuals.label,
              child: Icon(visuals.icon, size: 18, color: visuals.color),
            ),
            const SizedBox(width: 10),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    check.name,
                    style: theme.textTheme.bodyMedium?.copyWith(
                      fontWeight: check.isFailure
                          ? FontWeight.w600
                          : FontWeight.w400,
                    ),
                  ),
                  if (check.app.isNotEmpty || check.description.isNotEmpty)
                    Text(
                      [
                        if (check.app.isNotEmpty) check.app,
                        if (check.description.isNotEmpty) check.description,
                      ].join(' · '),
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                ],
              ),
            ),
            if (check.required)
              Container(
                margin: const EdgeInsets.only(left: 8),
                padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
                decoration: BoxDecoration(
                  color: theme.colorScheme.surfaceContainerHighest,
                  borderRadius: BorderRadius.circular(4),
                ),
                child: Text(
                  'Required',
                  style: TextStyle(
                    fontSize: 10,
                    fontWeight: FontWeight.w600,
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              ),
            if (_durationLabel() != null)
              Padding(
                padding: const EdgeInsets.only(left: 8),
                child: Text(
                  _durationLabel()!,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                    fontFeatures: const [],
                  ),
                ),
              ),
            if (hasUrl)
              IconButton(
                icon: const Icon(Icons.open_in_new, size: 16),
                tooltip: 'Open the check log',
                visualDensity: VisualDensity.compact,
                onPressed: () => onOpenUrl!(check.url),
              ),
          ],
        ),
      ),
    );
  }

  String? _durationLabel() {
    final d = check.duration;
    if (d == null) return null;
    if (d.inMinutes >= 1) return '${d.inMinutes}m ${d.inSeconds % 60}s';
    return '${d.inSeconds}s';
  }
}
