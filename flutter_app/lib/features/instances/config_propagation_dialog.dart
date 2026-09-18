import 'package:flutter/material.dart';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/api/api_client.dart';
import '../../core/api/cluster_api.dart';
import '../../core/instances/instances_providers.dart';
import '../../core/instances/models.dart';
import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';

/// Shows what differs between this hub and every instance, and offers to push
/// the shared configuration to all of them.
Future<void> showConfigPropagationDialog(BuildContext context, WidgetRef ref) {
  return showDialog<void>(
    context: context,
    builder: (context) => const _ConfigPropagationDialog(),
  );
}

class _ConfigPropagationDialog extends ConsumerStatefulWidget {
  const _ConfigPropagationDialog();

  @override
  ConsumerState<_ConfigPropagationDialog> createState() =>
      _ConfigPropagationDialogState();
}

class _ConfigPropagationDialogState
    extends ConsumerState<_ConfigPropagationDialog> {
  bool _pushing = false;
  PropagateReport? _report;
  String? _error;

  @override
  Widget build(BuildContext context) {
    final driftAsync = ref.watch(configDriftProvider);

    return AlertDialog(
      title: const AppText.sectionTitle('Configuration across instances'),
      content: SizedBox(
        width: 560,
        child: SingleChildScrollView(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              const AppText.muted(
                'Shared settings — prompts, review and merge policy, polling, '
                'per-repo and per-org overrides — are pushed to every '
                'instance. Machine-specific ones are never sent: the port, '
                'the bind address, GitHub and API tokens, local directories, '
                'and each instance’s own repository lists.',
              ),
              const SizedBox(height: 16),
              if (_report != null)
                _ReportView(report: _report!)
              else
                driftAsync.when(
                  loading: () => const Padding(
                    padding: EdgeInsets.all(24),
                    child: Center(child: CircularProgressIndicator()),
                  ),
                  error: (e, _) =>
                      AppText('Could not compare configuration: $e'),
                  data: (drifts) => _DriftView(drifts: drifts),
                ),
              if (_error != null) ...[
                const SizedBox(height: 12),
                AppText(_error!, color: AppColors.danger.resolve(context)),
              ],
            ],
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: _pushing ? null : () => Navigator.pop(context),
          child: Text(_report == null ? 'Cancel' : 'Close'),
        ),
        FilledButton(
          onPressed: _pushing ? null : _propagate,
          child: _pushing
              ? const SizedBox(
                  width: 16,
                  height: 16,
                  child: CircularProgressIndicator(strokeWidth: 2),
                )
              : const Text('Apply to all'),
        ),
      ],
    );
  }

  Future<void> _propagate() async {
    setState(() {
      _pushing = true;
      _error = null;
    });
    try {
      final report = await ref.read(hubApiClientProvider).propagateConfig();
      if (mounted) {
        setState(() {
          _report = report;
          _pushing = false;
        });
      }
      ref.invalidate(configDriftProvider);
    } on ApiException catch (e) {
      if (mounted) {
        setState(() {
          _error = e.message;
          _pushing = false;
        });
      }
    }
  }
}

class _DriftView extends StatelessWidget {
  const _DriftView({required this.drifts});

  final List<InstanceDrift> drifts;

  @override
  Widget build(BuildContext context) {
    final comparable = drifts.where((d) => !d.skipped).toList();
    if (comparable.isEmpty) {
      return const AppText('No other instances to compare against.');
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [for (final drift in comparable) _DriftTile(drift: drift)],
    );
  }
}

class _DriftTile extends StatelessWidget {
  const _DriftTile({required this.drift});

  final InstanceDrift drift;

  @override
  Widget build(BuildContext context) {
    if (drift.error.isNotEmpty) {
      return _StatusSurface(
        icon: Icons.cloud_off_outlined,
        iconColor: AppColors.danger.resolve(context),
        title: drift.displayName,
        message: drift.error,
        messageColor: AppColors.danger.resolve(context),
        badge: AppBadge(
          label: 'error',
          foreground: AppColors.danger.resolve(context),
          background: AppColors.danger.resolve(context).withValues(alpha: 0.12),
          border: AppColors.danger.resolve(context).withValues(alpha: 0.28),
        ),
      );
    }

    if (drift.inSync) {
      return _StatusSurface(
        icon: Icons.check_circle_outline,
        iconColor: AppColors.success.resolve(context),
        title: drift.displayName,
        message: 'In sync',
        badge: AppBadge(
          label: 'in sync',
          foreground: AppColors.success.resolve(context),
          background: AppColors.success
              .resolve(context)
              .withValues(alpha: 0.12),
          border: AppColors.success.resolve(context).withValues(alpha: 0.24),
        ),
      );
    }

    return Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: AppSurface(
        elevation: AppSurfaceElevation.raised,
        child: ExpansionTile(
          tilePadding: const EdgeInsets.symmetric(horizontal: 12, vertical: 2),
          childrenPadding: const EdgeInsets.fromLTRB(12, 0, 12, 12),
          leading: Icon(
            Icons.sync_problem_outlined,
            color: AppColors.warning.resolve(context),
          ),
          title: AppText.label(drift.displayName),
          subtitle: AppText.muted(
            drift.drifts.length == 1
                ? '1 setting differs'
                : '${drift.drifts.length} settings differ',
          ),
          trailing: AppBadge(
            label: drift.drifts.length == 1
                ? '1 diff'
                : '${drift.drifts.length} diffs',
            foreground: AppColors.warning.resolve(context),
            background: AppColors.warning
                .resolve(context)
                .withValues(alpha: 0.12),
            border: AppColors.warning.resolve(context).withValues(alpha: 0.24),
          ),
          children: [
            for (final d in drift.drifts)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 2),
                child: Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Expanded(flex: 2, child: AppText.mono(d.key, maxLines: 2)),
                    const SizedBox(width: 12),
                    Expanded(
                      flex: 3,
                      child: AppText.muted(
                        d.missing
                            ? 'not set → ${_render(d.hubValue)}'
                            : '${_render(d.remoteValue)} → ${_render(d.hubValue)}',
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

  static String _render(Object? value) {
    if (value == null) return 'unset';
    final text = '$value';
    return text.length > 60 ? '${text.substring(0, 57)}…' : text;
  }
}

class _ReportView extends StatelessWidget {
  const _ReportView({required this.report});

  final PropagateReport report;

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        for (final result in report.results)
          _StatusSurface(
            icon: result.skipped
                ? Icons.remove_circle_outline
                : result.ok
                ? Icons.check_circle_outline
                : Icons.error_outline,
            iconColor: result.skipped
                ? AppColors.textMuted.resolve(context)
                : result.ok
                ? AppColors.success.resolve(context)
                : AppColors.danger.resolve(context),
            title: result.displayName,
            message: result.error.isNotEmpty
                ? result.error
                : result.appliedKeys.isEmpty
                ? 'Applied'
                : 'Applied ${result.appliedKeys.length} settings',
            messageColor: result.ok ? null : AppColors.danger.resolve(context),
            badge: AppBadge(
              label: result.skipped
                  ? 'skipped'
                  : result.ok
                  ? 'applied'
                  : 'failed',
              foreground: result.skipped
                  ? AppColors.textMuted.resolve(context)
                  : result.ok
                  ? AppColors.success.resolve(context)
                  : AppColors.danger.resolve(context),
              background:
                  (result.skipped
                          ? AppColors.surfaceRaised
                          : result.ok
                          ? AppColors.success
                          : AppColors.danger)
                      .resolve(context)
                      .withValues(alpha: 0.12),
              border:
                  (result.skipped
                          ? AppColors.border
                          : result.ok
                          ? AppColors.success
                          : AppColors.danger)
                      .resolve(context)
                      .withValues(alpha: 0.24),
            ),
          ),
        if (report.skippedLocal.isNotEmpty) ...[
          const SizedBox(height: 8),
          AppText.muted('Kept local: ${report.skippedLocal.join(', ')}'),
        ],
      ],
    );
  }
}

class _StatusSurface extends StatelessWidget {
  const _StatusSurface({
    required this.icon,
    required this.iconColor,
    required this.title,
    required this.message,
    required this.badge,
    this.messageColor,
  });

  final IconData icon;
  final Color iconColor;
  final String title;
  final String message;
  final Color? messageColor;
  final Widget badge;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: AppSurface(
        elevation: AppSurfaceElevation.raised,
        padding: const EdgeInsets.all(12),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Icon(icon, color: iconColor),
            const SizedBox(width: 10),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  AppText.label(title),
                  const SizedBox(height: 4),
                  AppText(
                    message,
                    role: AppTextRole.bodyMuted,
                    color: messageColor,
                  ),
                ],
              ),
            ),
            const SizedBox(width: 8),
            badge,
          ],
        ),
      ),
    );
  }
}
