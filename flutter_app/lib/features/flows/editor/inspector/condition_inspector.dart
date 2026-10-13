import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/editor/inspector/inspector_parts.dart';
import 'package:heimdallm/shared/design_system/components/components.dart';

const _days = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];
const _windows = ['session', 'weekly', 'monthly', 'credit', 'any'];

/// Properties of the selected condition step: how its conditions combine,
/// and its schedule and quota conditions.
class ConditionInspector extends ConsumerWidget {
  final FlowNode node;
  final List<FlowAgent> agents;
  final List<FlowIssue> issues;

  /// The built-in default flow: values show, nothing can be changed.
  final bool readOnly;

  const ConditionInspector({
    super.key,
    required this.node,
    required this.agents,
    this.issues = const [],
    this.readOnly = false,
  });

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final ctl = ref.read(flowEditorProvider.notifier);
    void update(FlowNode n) {
      if (!readOnly) ctl.updateNode(n);
    }

    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        InspectorHeader(
          icon: Icons.call_split,
          accent: Theme.of(context).colorScheme.tertiary,
          title: 'Condition step',
          id: node.id,
          description:
              'Continues through "yes" when its conditions hold, "no" '
              'otherwise. Unknown quota counts as not holding.',
        ),
        if (readOnly) const InspectorReadOnlyNote(),
        const InspectorLabel('Holds when'),
        DropdownButtonFormField<String>(
          key: const ValueKey('inspector-match'),
          // ignore: deprecated_member_use
          value: node.match,
          isExpanded: true,
          style: Theme.of(context).textTheme.bodyMedium,
          decoration: const InputDecoration(
            isDense: true,
            border: OutlineInputBorder(),
          ),
          items: const [
            DropdownMenuItem(value: 'all', child: Text('All conditions')),
            DropdownMenuItem(value: 'any', child: Text('Any condition')),
          ],
          onChanged: readOnly
              ? null
              : (v) => update(node.copyWith(match: v ?? 'all')),
        ),
        const InspectorLabel('Schedule'),
        for (var i = 0; i < node.schedule.length; i++)
          _ScheduleEditor(
            index: i,
            readOnly: readOnly,
            value: node.schedule[i],
            onChanged: (s) =>
                update(node.copyWith(schedule: [...node.schedule]..[i] = s)),
            onRemove: () => update(
              node.copyWith(schedule: [...node.schedule]..removeAt(i)),
            ),
          ),
        Align(
          alignment: Alignment.centerLeft,
          child: TextButton.icon(
            key: const ValueKey('inspector-add-schedule'),
            onPressed: readOnly
                ? null
                : () => update(
                    node.copyWith(
                      schedule: [...node.schedule, const ScheduleCondition()],
                    ),
                  ),
            icon: const Icon(Icons.schedule, size: 18),
            label: const Text('Add schedule'),
          ),
        ),
        const InspectorLabel('Quota'),
        for (var i = 0; i < node.quota.length; i++)
          _QuotaEditor(
            index: i,
            readOnly: readOnly,
            value: node.quota[i],
            agents: agents,
            onChanged: (q) =>
                update(node.copyWith(quota: [...node.quota]..[i] = q)),
            onRemove: () =>
                update(node.copyWith(quota: [...node.quota]..removeAt(i))),
          ),
        Align(
          alignment: Alignment.centerLeft,
          child: TextButton.icon(
            key: const ValueKey('inspector-add-quota'),
            onPressed: readOnly
                ? null
                : () => update(
                    node.copyWith(
                      quota: [
                        ...node.quota,
                        QuotaCondition(
                          agent:
                              agents
                                  .where((a) => a.installed)
                                  .firstOrNull
                                  ?.id ??
                              'claude',
                        ),
                      ],
                    ),
                  ),
            icon: const Icon(Icons.data_usage, size: 18),
            label: const Text('Add quota condition'),
          ),
        ),
        InspectorIssues(issues: issues),
      ],
    );
  }
}

/// A bordered group for one condition, with a remove button in its corner.
class _ConditionCard extends StatelessWidget {
  final String title;
  final VoidCallback? onRemove;
  final List<Widget> children;

  const _ConditionCard({
    required this.title,
    required this.onRemove,
    required this.children,
  });

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.only(bottom: 8),
    child: AppSurface(
      elevation: AppSurfaceElevation.raised,
      padding: const EdgeInsets.fromLTRB(12, 4, 4, 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              Expanded(child: AppText.label(title)),
              IconButton(
                tooltip: 'Remove',
                visualDensity: VisualDensity.compact,
                icon: const Icon(Icons.close, size: 16),
                onPressed: onRemove,
              ),
            ],
          ),
          Padding(
            padding: const EdgeInsets.only(right: 8),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: children,
            ),
          ),
        ],
      ),
    ),
  );
}

class _ScheduleEditor extends StatelessWidget {
  final int index;
  final bool readOnly;
  final ScheduleCondition value;
  final ValueChanged<ScheduleCondition> onChanged;
  final VoidCallback onRemove;

  const _ScheduleEditor({
    required this.index,
    required this.readOnly,
    required this.value,
    required this.onChanged,
    required this.onRemove,
  });

  @override
  Widget build(BuildContext context) {
    final hhmm = [
      FilteringTextInputFormatter.allow(RegExp(r'[0-9:]')),
      LengthLimitingTextInputFormatter(5),
    ];
    return _ConditionCard(
      title: value.days.isEmpty ? 'Every day' : 'On selected days',
      onRemove: readOnly ? null : onRemove,
      children: [
        Wrap(
          spacing: 4,
          runSpacing: 4,
          children: [
            for (final d in _days)
              FilterChip(
                label: Text(d),
                visualDensity: VisualDensity.compact,
                showCheckmark: false,
                selected: value.days.contains(d),
                onSelected: readOnly
                    ? null
                    : (on) => onChanged(
                        value.copyWith(
                          days: on
                              ? [
                                  ..._days.where(
                                    (x) => x == d || value.days.contains(x),
                                  ),
                                ]
                              : value.days.where((x) => x != d).toList(),
                        ),
                      ),
              ),
          ],
        ),
        const SizedBox(height: 12),
        Row(
          children: [
            Expanded(
              child: SyncedTextField(
                key: ValueKey('schedule-$index-from'),
                value: value.from,
                label: 'From',
                hint: 'HH:MM',
                inputFormatters: hhmm,
                enabled: !readOnly,
                onChanged: (v) => onChanged(value.copyWith(from: v)),
              ),
            ),
            const SizedBox(width: 8),
            Expanded(
              child: SyncedTextField(
                key: ValueKey('schedule-$index-to'),
                value: value.to,
                label: 'To',
                hint: 'HH:MM',
                inputFormatters: hhmm,
                enabled: !readOnly,
                onChanged: (v) => onChanged(value.copyWith(to: v)),
              ),
            ),
          ],
        ),
        Align(
          alignment: Alignment.centerLeft,
          child: TextButton(
            key: ValueKey('schedule-$index-allday'),
            onPressed: readOnly
                ? null
                : () => onChanged(value.copyWith(from: '00:00', to: '00:00')),
            child: const Text('All day'),
          ),
        ),
        Text(
          'Same start and end = all day',
          style: Theme.of(context).textTheme.bodySmall,
        ),
        const SizedBox(height: 12),
        SyncedTextField(
          key: ValueKey('schedule-$index-tz'),
          value: value.tz,
          label: 'Time zone',
          hint: 'IANA, e.g. Europe/Madrid · empty = daemon time',
          enabled: !readOnly,
          same: (text, v) => text.trim() == v,
          onChanged: (v) => onChanged(value.copyWith(tz: v.trim())),
        ),
      ],
    );
  }
}

class _QuotaEditor extends StatelessWidget {
  final int index;
  final bool readOnly;
  final QuotaCondition value;
  final List<FlowAgent> agents;
  final ValueChanged<QuotaCondition> onChanged;
  final VoidCallback onRemove;

  const _QuotaEditor({
    required this.index,
    required this.readOnly,
    required this.value,
    required this.agents,
    required this.onChanged,
    required this.onRemove,
  });

  @override
  Widget build(BuildContext context) {
    const dense = InputDecoration(isDense: true, border: OutlineInputBorder());
    final ids = [
      for (final a in agents) a.id,
      if (value.agent.isNotEmpty && !agents.any((a) => a.id == value.agent))
        value.agent,
    ];
    // A model window (`model:<id>`) is kept as written, offered as itself.
    final windows = [
      ..._windows,
      if (!_windows.contains(value.window)) value.window,
    ];
    return _ConditionCard(
      title: value.describe(),
      onRemove: readOnly ? null : onRemove,
      children: [
        DropdownButtonFormField<String>(
          key: ValueKey('quota-$index-agent'),
          // ignore: deprecated_member_use
          value: value.agent.isEmpty ? null : value.agent,
          isExpanded: true,
          style: Theme.of(context).textTheme.bodyMedium,
          decoration: dense.copyWith(labelText: 'Agent'),
          items: [
            for (final id in ids) DropdownMenuItem(value: id, child: Text(id)),
          ],
          onChanged: readOnly
              ? null
              : (v) => onChanged(value.copyWith(agent: v)),
        ),
        const SizedBox(height: 12),
        DropdownButtonFormField<String>(
          key: ValueKey('quota-$index-window'),
          // ignore: deprecated_member_use
          value: value.window,
          isExpanded: true,
          style: Theme.of(context).textTheme.bodyMedium,
          decoration: dense.copyWith(labelText: 'Window'),
          items: [
            for (final w in windows) DropdownMenuItem(value: w, child: Text(w)),
          ],
          onChanged: readOnly
              ? null
              : (v) => onChanged(value.copyWith(window: v)),
        ),
        const SizedBox(height: 12),
        Row(
          children: [
            Expanded(
              child: DropdownButtonFormField<String>(
                key: ValueKey('quota-$index-op'),
                // ignore: deprecated_member_use
                value: value.op,
                isExpanded: true,
                style: Theme.of(context).textTheme.bodyMedium,
                decoration: dense.copyWith(labelText: 'Used'),
                items: const [
                  DropdownMenuItem(value: 'below', child: Text('below')),
                  DropdownMenuItem(value: 'above', child: Text('above')),
                ],
                onChanged: readOnly
                    ? null
                    : (v) => onChanged(value.copyWith(op: v)),
              ),
            ),
            const SizedBox(width: 8),
            SizedBox(
              width: 88,
              child: SyncedTextField(
                key: ValueKey('quota-$index-percent'),
                value: formatPercent(value.percent),
                label: '%',
                enabled: !readOnly,
                keyboardType: const TextInputType.numberWithOptions(
                  decimal: true,
                ),
                inputFormatters: [
                  FilteringTextInputFormatter.allow(RegExp(r'[0-9.]')),
                ],
                same: (text, v) => double.tryParse(text) == double.tryParse(v),
                onChanged: (v) => onChanged(
                  value.copyWith(percent: double.tryParse(v) ?? value.percent),
                ),
              ),
            ),
          ],
        ),
      ],
    );
  }
}
