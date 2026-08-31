import 'package:flutter/material.dart';

import '../../core/models/config_model.dart';
import 'override_field.dart';

/// Scoped editor for the merge-tracking settings that can vary by
/// organisation and repository.
///
/// [inherited] is the effective parent configuration. For a repository that
/// already includes the organisation override; [parentOverride] is only used
/// to explain whether each inherited value came from the organisation or the
/// global settings.
class MergeTrackingOverrideEditor extends StatelessWidget {
  final MergeTrackingOverride value;
  final MergeTrackingConfig inherited;
  final MergeTrackingOverride? parentOverride;
  final String parentLabel;
  final String? enabledInheritedLabel;
  final String scopeKey;
  final ValueChanged<MergeTrackingOverride> onChanged;

  const MergeTrackingOverrideEditor({
    super.key,
    required this.value,
    required this.inherited,
    required this.onChanged,
    required this.scopeKey,
    this.parentOverride,
    this.parentLabel = 'global',
    this.enabledInheritedLabel,
  });

  String _source(Object? parentValue) =>
      parentValue != null ? parentLabel : 'global';

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _OverrideSwitch(
          fieldKey: '${scopeKey}_merge_tracking_switch',
          resetKey: '${scopeKey}_merge_tracking_reset',
          label: 'Track my pull requests',
          helper:
              'Watch your open pull requests and report what blocks each merge',
          inheritedValue: inherited.enabled,
          inheritedLabel:
              enabledInheritedLabel ?? _source(parentOverride?.enabled),
          overrideValue: value.enabled,
          onChanged: (v) => onChanged(value.copyWith(enabled: v)),
        ),
        const SizedBox(height: 10),
        _OverrideSwitch(
          fieldKey: '${scopeKey}_merge_tracking_include_assigned',
          label: 'Include PRs assigned to me',
          helper: 'Also track PRs someone else opened but assigned to you',
          inheritedValue: inherited.includeAssigned,
          inheritedLabel: _source(parentOverride?.includeAssigned),
          overrideValue: value.includeAssigned,
          onChanged: (v) => onChanged(value.copyWith(includeAssigned: v)),
        ),
        const Divider(height: 24),
        Text(
          'Automations',
          style: Theme.of(
            context,
          ).textTheme.labelLarge?.copyWith(fontWeight: FontWeight.w700),
        ),
        const SizedBox(height: 8),
        _OverrideSwitch(
          fieldKey: '${scopeKey}_merge_tracking_enable_auto_merge',
          label: 'Turn on auto-merge',
          helper: "Enable GitHub's native auto-merge when requirements pass",
          inheritedValue: inherited.enableAutoMerge,
          inheritedLabel: _source(parentOverride?.enableAutoMerge),
          overrideValue: value.enableAutoMerge,
          onChanged: (v) => onChanged(value.copyWith(enableAutoMerge: v)),
        ),
        const SizedBox(height: 10),
        _OverrideSwitch(
          fieldKey: '${scopeKey}_merge_tracking_update_branch',
          label: 'Update stale branches',
          helper: 'Bring a PR up to date when it falls behind its base branch',
          inheritedValue: inherited.updateBranch,
          inheritedLabel: _source(parentOverride?.updateBranch),
          overrideValue: value.updateBranch,
          onChanged: (v) => onChanged(value.copyWith(updateBranch: v)),
        ),
        const SizedBox(height: 10),
        _OverrideSwitch(
          fieldKey: '${scopeKey}_merge_tracking_resolve_conflicts',
          label: 'Resolve conflicts',
          helper:
              'Let the configured agent resolve conflicts and force-push the branch',
          inheritedValue: inherited.resolveConflicts,
          inheritedLabel: _source(parentOverride?.resolveConflicts),
          overrideValue: value.resolveConflicts,
          onChanged: (v) => onChanged(value.copyWith(resolveConflicts: v)),
        ),
        const SizedBox(height: 10),
        _OverrideSwitch(
          fieldKey: '${scopeKey}_merge_tracking_merge',
          label: 'Merge when ready',
          helper: 'Merge once every requirement has been re-checked and passes',
          inheritedValue: inherited.merge,
          inheritedLabel: _source(parentOverride?.merge),
          overrideValue: value.merge,
          onChanged: (v) => onChanged(value.copyWith(merge: v)),
        ),
        const SizedBox(height: 10),
        _OverrideSwitch(
          fieldKey: '${scopeKey}_merge_tracking_require_approval',
          label: 'Require an approving review',
          helper: 'Never merge without approval, even if the repo allows it',
          inheritedValue: inherited.requireApproval,
          inheritedLabel: _source(parentOverride?.requireApproval),
          overrideValue: value.requireApproval,
          onChanged: (v) => onChanged(value.copyWith(requireApproval: v)),
        ),
        const SizedBox(height: 10),
        OverrideDropdown(
          key: Key('${scopeKey}_merge_tracking_merge_method'),
          label: 'Merge method',
          globalValue: inherited.mergeMethod,
          inheritedLabel: _source(parentOverride?.mergeMethod),
          overrideValue: value.mergeMethod,
          options: const ['squash', 'merge', 'rebase'],
          onChanged: (v) => onChanged(value.copyWith(mergeMethod: v)),
        ),
        const SizedBox(height: 10),
        OverrideTextField(
          key: Key('${scopeKey}_merge_tracking_resolve_timeout'),
          label: 'Conflict-resolution timeout',
          helper: 'Wall clock for one agent run, e.g. 30m',
          globalValue: inherited.resolveTimeout,
          inheritedLabel: _source(parentOverride?.resolveTimeout),
          overrideValue: value.resolveTimeout,
          onChanged: (v) => onChanged(value.copyWith(resolveTimeout: v)),
        ),
        const SizedBox(height: 10),
        OverrideDropdown(
          key: Key('${scopeKey}_merge_tracking_resolve_effort'),
          label: 'Conflict-resolution effort',
          globalValue: inherited.resolveEffort,
          inheritedLabel: _source(parentOverride?.resolveEffort),
          overrideValue: value.resolveEffort,
          options: const ['low', 'medium', 'high', 'max'],
          onChanged: (v) => onChanged(value.copyWith(resolveEffort: v)),
        ),
        const Divider(height: 24),
        Text(
          'Advanced limits',
          style: Theme.of(
            context,
          ).textTheme.labelLarge?.copyWith(fontWeight: FontWeight.w700),
        ),
        const SizedBox(height: 8),
        _intField(
          key: '${scopeKey}_merge_tracking_max_update_attempts',
          label: 'Maximum branch-update attempts',
          inheritedValue: inherited.maxUpdateAttempts,
          inheritedLabel: _source(parentOverride?.maxUpdateAttempts),
          overrideValue: value.maxUpdateAttempts,
          onChanged: (v) => onChanged(value.copyWith(maxUpdateAttempts: v)),
        ),
        const SizedBox(height: 10),
        _intField(
          key: '${scopeKey}_merge_tracking_max_resolve_attempts',
          label: 'Maximum conflict-resolution attempts',
          inheritedValue: inherited.maxResolveAttempts,
          inheritedLabel: _source(parentOverride?.maxResolveAttempts),
          overrideValue: value.maxResolveAttempts,
          onChanged: (v) => onChanged(value.copyWith(maxResolveAttempts: v)),
        ),
        const SizedBox(height: 10),
        _intField(
          key: '${scopeKey}_merge_tracking_max_merge_attempts',
          label: 'Maximum merge attempts',
          inheritedValue: inherited.maxMergeAttempts,
          inheritedLabel: _source(parentOverride?.maxMergeAttempts),
          overrideValue: value.maxMergeAttempts,
          onChanged: (v) => onChanged(value.copyWith(maxMergeAttempts: v)),
        ),
        const SizedBox(height: 10),
        OverrideTextField(
          key: Key('${scopeKey}_merge_tracking_action_cooldown'),
          label: 'Action cooldown',
          helper: 'Minimum delay between write actions on one PR, e.g. 10m',
          globalValue: inherited.actionCooldown,
          inheritedLabel: _source(parentOverride?.actionCooldown),
          overrideValue: value.actionCooldown,
          onChanged: (v) => onChanged(value.copyWith(actionCooldown: v)),
        ),
        const SizedBox(height: 10),
        Text(
          'Check interval and PRs per tick stay global because one poller '
          'serves every repository.',
          style: Theme.of(context).textTheme.bodySmall?.copyWith(
            color: Theme.of(context).colorScheme.onSurfaceVariant,
          ),
        ),
      ],
    );
  }

  Widget _intField({
    required String key,
    required String label,
    required int inheritedValue,
    required String inheritedLabel,
    required int? overrideValue,
    required ValueChanged<int?> onChanged,
  }) => OverrideTextField(
    key: Key(key),
    label: label,
    globalValue: inheritedValue.toString(),
    inheritedLabel: inheritedLabel,
    overrideValue: overrideValue?.toString(),
    onChanged: (v) {
      if (v == null) {
        onChanged(null);
        return;
      }
      final parsed = int.tryParse(v);
      if (parsed != null && parsed >= 0) onChanged(parsed);
    },
  );
}

class _OverrideSwitch extends StatelessWidget {
  final String fieldKey;
  final String? resetKey;
  final String label;
  final String helper;
  final bool inheritedValue;
  final String inheritedLabel;
  final bool? overrideValue;
  final ValueChanged<bool?> onChanged;

  const _OverrideSwitch({
    required this.fieldKey,
    this.resetKey,
    required this.label,
    required this.helper,
    required this.inheritedValue,
    required this.inheritedLabel,
    required this.overrideValue,
    required this.onChanged,
  });

  @override
  Widget build(BuildContext context) {
    final overridden = overrideValue != null;
    final effectiveValue = overrideValue ?? inheritedValue;
    return Container(
      decoration: BoxDecoration(
        color: Theme.of(
          context,
        ).colorScheme.surfaceContainerHighest.withValues(alpha: 0.3),
        borderRadius: BorderRadius.circular(6),
        border: Border(
          left: BorderSide(
            width: 3,
            color: overridden ? Colors.green.shade600 : Colors.transparent,
          ),
        ),
      ),
      padding: const EdgeInsets.all(10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(label, style: const TextStyle(fontSize: 13)),
                    const SizedBox(height: 2),
                    Text(
                      helper,
                      style: Theme.of(context).textTheme.bodySmall?.copyWith(
                        color: Theme.of(context).colorScheme.onSurfaceVariant,
                      ),
                    ),
                  ],
                ),
              ),
              const SizedBox(width: 12),
              Switch(
                key: Key(fieldKey),
                value: effectiveValue,
                onChanged: (v) => onChanged(v),
              ),
            ],
          ),
          const SizedBox(height: 4),
          Row(
            children: [
              Text(
                overridden
                    ? 'Overridden at this level'
                    : 'Inherited from $inheritedLabel',
                style: Theme.of(context).textTheme.bodySmall,
              ),
              const Spacer(),
              if (overridden)
                TextButton(
                  key: Key(resetKey ?? '${fieldKey}_reset'),
                  onPressed: () => onChanged(null),
                  child: const Text('Use inherited'),
                ),
            ],
          ),
        ],
      ),
    );
  }
}
