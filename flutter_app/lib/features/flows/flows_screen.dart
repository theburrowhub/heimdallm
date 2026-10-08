import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../core/models/flow.dart';
import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/toast.dart';
import '../config/config_providers.dart';
import '../dashboard/dashboard_providers.dart';

/// Every review flow and the global selection.
final flowsProvider = FutureProvider.autoDispose<FlowListing>((ref) {
  return ref.watch(apiClientProvider).fetchFlows();
});

const flowWeekdays = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];
const flowQuotaWindows = ['session', 'weekly', 'monthly', 'credit', 'any'];

/// Human summary of one rule.
String describeRule(FlowRule r) {
  if (r.isCatchAll) return '${r.agent} — always (fallback)';
  final parts = [
    ...r.schedule.map((s) => s.describe()),
    ...r.quota.map((q) => q.describe()),
  ];
  return '${r.agent} — when ${parts.join(r.match == 'any' ? ' or ' : ' and ')}';
}

/// List of flows.
class FlowsScreen extends ConsumerWidget {
  const FlowsScreen({super.key});

  Future<void> _create(BuildContext context, FlowListing listing) async {
    final ctrl = TextEditingController();
    final id = await showDialog<String>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('New flow'),
        content: TextField(
          key: const ValueKey('new-flow-id'),
          controller: ctrl,
          autofocus: true,
          decoration: const InputDecoration(
            labelText: 'Flow id',
            hintText: 'e.g. weekday-copilot',
            helperText: 'Lowercase letters, digits, - and _',
          ),
          inputFormatters: [
            FilteringTextInputFormatter.allow(RegExp(r'[a-z0-9_-]')),
            LengthLimitingTextInputFormatter(64),
          ],
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('Cancel'),
          ),
          FilledButton(
            key: const ValueKey('new-flow-create'),
            onPressed: () => Navigator.pop(ctx, ctrl.text.trim()),
            child: const Text('Create'),
          ),
        ],
      ),
    );
    if (id == null || id.isEmpty || !context.mounted) return;
    if (id == listing.defaultId || listing.flows.containsKey(id)) {
      showToast(context, 'A flow named "$id" already exists', isError: true);
      return;
    }
    context.go('/flows/$id');
  }

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final async = ref.watch(flowsProvider);
    return async.when(
      loading: () => const Center(child: CircularProgressIndicator()),
      error: (e, _) => Center(child: AppText('Could not load flows: $e')),
      data: (listing) => ListView(
        padding: const EdgeInsets.all(16),
        children: [
          Row(
            children: [
              Expanded(
                child: AppText.muted(
                  'A flow decides which agent reviews a PR. Its rules are '
                  'checked top to bottom: the first rule whose conditions '
                  'hold and whose agent is installed reviews; the next ones '
                  'take over if that agent runs out of quota.',
                ),
              ),
              const SizedBox(width: 12),
              FilledButton.icon(
                key: const ValueKey('flows-new'),
                onPressed: () => _create(context, listing),
                icon: const Icon(Icons.add, size: 18),
                label: const Text('New flow'),
              ),
            ],
          ),
          const SizedBox(height: 16),
          for (final id in listing.ids)
            Padding(
              padding: const EdgeInsets.only(bottom: 12),
              child: _FlowCard(
                id: id,
                flow: listing.flows[id]!,
                isDefault: id == listing.defaultId,
                selected: id == listing.selected,
              ),
            ),
        ],
      ),
    );
  }
}

class _FlowCard extends StatelessWidget {
  final String id;
  final ReviewFlow flow;
  final bool isDefault;
  final bool selected;

  const _FlowCard({
    required this.id,
    required this.flow,
    required this.isDefault,
    required this.selected,
  });

  @override
  Widget build(BuildContext context) {
    final ok = AppColors.success.resolve(context);
    final muted = AppColors.textMuted.resolve(context);
    final border = AppColors.border.resolve(context);
    return InkWell(
      key: ValueKey('flow-card-$id'),
      borderRadius: BorderRadius.circular(12),
      onTap: () => context.go('/flows/$id'),
      child: AppSurface(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                const Icon(Icons.alt_route, size: 20),
                const SizedBox(width: 8),
                Expanded(
                  child: AppText.sectionTitle(
                    flow.name.isEmpty ? id : flow.name,
                  ),
                ),
                if (selected)
                  AppBadge(
                    label: 'Used globally',
                    foreground: ok,
                    background: ok.withValues(alpha: 0.14),
                    border: ok.withValues(alpha: 0.35),
                  ),
                if (isDefault) ...[
                  const SizedBox(width: 6),
                  AppBadge(
                    label: 'Primary / fallback',
                    foreground: muted,
                    background: AppColors.surfaceRaised.resolve(context),
                    border: border,
                  ),
                ],
                const Icon(Icons.chevron_right, size: 18),
              ],
            ),
            const SizedBox(height: 8),
            if (flow.rules.isEmpty)
              AppText.muted('No rules yet.')
            else
              for (var i = 0; i < flow.rules.length; i++)
                Padding(
                  padding: const EdgeInsets.only(top: 2),
                  child: Text('${i + 1}. ${describeRule(flow.rules[i])}'),
                ),
          ],
        ),
      ),
    );
  }
}

/// Edits one flow (the default flow edits primary/fallback) and simulates it.
class FlowEditorScreen extends ConsumerStatefulWidget {
  final String flowId;

  const FlowEditorScreen({super.key, required this.flowId});

  @override
  ConsumerState<FlowEditorScreen> createState() => _FlowEditorScreenState();
}

class _FlowEditorScreenState extends ConsumerState<FlowEditorScreen> {
  ReviewFlow? _flow;
  final _nameCtrl = TextEditingController();
  bool _saving = false;
  FlowDecision? _decision;
  // Stable identities for the rule cards, parallel to _flow.rules, so a card
  // keeps its state (and text focus) while edited and follows its rule when
  // rules are reordered or deleted.
  List<int> _ruleIds = [];
  int _nextRuleId = 0;

  @override
  void dispose() {
    _nameCtrl.dispose();
    super.dispose();
  }

  void _init(FlowListing listing) {
    if (_flow != null) return;
    _flow = listing.flows[widget.flowId] ?? const ReviewFlow();
    _ruleIds = [for (final _ in _flow!.rules) _nextRuleId++];
    _nameCtrl.text = _flow!.name;
  }

  void _update(ReviewFlow f) => setState(() => _flow = f);

  void _updateRule(int i, FlowRule r) {
    final rules = [..._flow!.rules];
    rules[i] = r;
    _update(_flow!.copyWith(rules: rules));
  }

  Future<void> _save() async {
    final flow = _flow!.copyWith(name: _nameCtrl.text.trim());
    if (flow.rules.isEmpty) {
      showToast(context, 'Add at least one rule', isError: true);
      return;
    }
    setState(() => _saving = true);
    try {
      final json = await ref
          .read(apiClientProvider)
          .putFlow(widget.flowId, flow);
      ref.read(configNotifierProvider.notifier).updateFromServer(json);
      ref.invalidate(flowsProvider);
      if (mounted) showToast(context, 'Flow saved');
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  Future<void> _delete() async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Delete flow?'),
        content: const Text(
          'Repositories and organizations using it go back to their '
          "parent's flow.",
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            key: const ValueKey('flow-delete-confirm'),
            onPressed: () => Navigator.pop(ctx, true),
            child: const Text('Delete'),
          ),
        ],
      ),
    );
    if (ok != true || !mounted) return;
    try {
      final json = await ref.read(apiClientProvider).deleteFlow(widget.flowId);
      ref.read(configNotifierProvider.notifier).updateFromServer(json);
      ref.invalidate(flowsProvider);
      if (mounted) {
        showToast(context, 'Flow deleted');
        context.go('/flows');
      }
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _useGlobally(bool use) async {
    try {
      final json = await ref.read(apiClientProvider).patchConfig({
        'ai': {'flow': use ? widget.flowId : ''},
      });
      ref.read(configNotifierProvider.notifier).updateFromServer(json);
      ref.invalidate(flowsProvider);
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _setLegacy(String key, String value) async {
    try {
      final json = await ref.read(apiClientProvider).patchConfig({
        'ai': {key: value},
      });
      ref.read(configNotifierProvider.notifier).updateFromServer(json);
      ref.invalidate(flowsProvider);
      setState(() => _flow = null);
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _simulate() async {
    try {
      final d = await ref
          .read(apiClientProvider)
          .simulateFlow(flow: widget.flowId);
      if (mounted) setState(() => _decision = d);
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  @override
  Widget build(BuildContext context) {
    final async = ref.watch(flowsProvider);
    return async.when(
      loading: () => const Center(child: CircularProgressIndicator()),
      error: (e, _) => Center(child: AppText('Could not load flows: $e')),
      data: (listing) {
        _init(listing);
        final isDefault = widget.flowId == listing.defaultId;
        final exists = listing.flows.containsKey(widget.flowId);
        final agents = listing.agents.isEmpty
            ? const ['claude', 'codex', 'gemini']
            : listing.agents;
        return ListView(
          padding: const EdgeInsets.all(16),
          children: [
            Row(
              children: [
                IconButton(
                  key: const ValueKey('flow-back'),
                  tooltip: 'All flows',
                  icon: const Icon(Icons.arrow_back),
                  onPressed: () => context.go('/flows'),
                ),
                Expanded(child: AppText.sectionTitle(widget.flowId)),
                if (exists)
                  Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      const Text('Use globally'),
                      Switch(
                        key: const ValueKey('flow-use-globally'),
                        value: listing.selected == widget.flowId,
                        onChanged: _useGlobally,
                      ),
                    ],
                  ),
              ],
            ),
            const SizedBox(height: 8),
            if (isDefault)
              _defaultEditor(listing, agents)
            else
              ..._ruleEditor(agents, exists),
            const SizedBox(height: 16),
            _simulation(),
          ],
        );
      },
    );
  }

  Widget _defaultEditor(FlowListing listing, List<String> agents) {
    final rules = listing.flows[listing.defaultId]?.rules ?? const [];
    final primary = rules.isNotEmpty ? rules[0].agent : '';
    final fallback = rules.length > 1 ? rules[1].agent : '';
    return AppSurface(
      padding: const EdgeInsets.all(14),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          AppText.muted(
            'The default flow is the classic primary/fallback pair: the '
            'primary reviews, and the fallback takes over if the primary is '
            'not installed or runs out of quota. Create a flow for time or '
            'quota rules.',
          ),
          const SizedBox(height: 12),
          DropdownButtonFormField<String>(
            key: const ValueKey('default-flow-primary'),
            initialValue: agents.contains(primary) ? primary : null,
            decoration: const InputDecoration(
              labelText: 'Primary agent',
              border: OutlineInputBorder(),
              isDense: true,
            ),
            items: [
              for (final a in agents)
                DropdownMenuItem(value: a, child: Text(a)),
            ],
            onChanged: (v) => v == null ? null : _setLegacy('primary', v),
          ),
          const SizedBox(height: 12),
          DropdownButtonFormField<String>(
            key: const ValueKey('default-flow-fallback'),
            initialValue: agents.contains(fallback) ? fallback : 'none',
            decoration: const InputDecoration(
              labelText: 'Fallback agent',
              border: OutlineInputBorder(),
              isDense: true,
            ),
            items: [
              const DropdownMenuItem(value: 'none', child: Text('none')),
              for (final a in agents)
                DropdownMenuItem(value: a, child: Text(a)),
            ],
            onChanged: (v) =>
                _setLegacy('fallback', v == null || v == 'none' ? '' : v),
          ),
        ],
      ),
    );
  }

  List<Widget> _ruleEditor(List<String> agents, bool exists) {
    final flow = _flow!;
    return [
      TextField(
        key: const ValueKey('flow-name'),
        controller: _nameCtrl,
        decoration: const InputDecoration(
          labelText: 'Name',
          border: OutlineInputBorder(),
          isDense: true,
        ),
      ),
      const SizedBox(height: 12),
      ReorderableListView(
        shrinkWrap: true,
        physics: const NeverScrollableScrollPhysics(),
        buildDefaultDragHandles: true,
        onReorder: (from, to) {
          final rules = [...flow.rules];
          if (to > from) to--;
          rules.insert(to, rules.removeAt(from));
          _ruleIds.insert(to, _ruleIds.removeAt(from));
          _update(flow.copyWith(rules: rules));
        },
        children: [
          for (var i = 0; i < flow.rules.length; i++)
            Padding(
              key: ValueKey('flow-rule-${_ruleIds[i]}'),
              padding: const EdgeInsets.only(bottom: 10),
              child: FlowRuleCard(
                index: i,
                rule: flow.rules[i],
                agents: agents,
                onChanged: (r) => _updateRule(i, r),
                onDelete: () {
                  _ruleIds.removeAt(i);
                  _update(flow.copyWith(rules: [...flow.rules]..removeAt(i)));
                },
              ),
            ),
        ],
      ),
      Row(
        children: [
          OutlinedButton.icon(
            key: const ValueKey('flow-add-rule'),
            onPressed: () {
              _ruleIds.add(_nextRuleId++);
              _update(
                flow.copyWith(
                  rules: [
                    ...flow.rules,
                    FlowRule(agent: agents.first),
                  ],
                ),
              );
            },
            icon: const Icon(Icons.add, size: 18),
            label: const Text('Add rule'),
          ),
          const Spacer(),
          if (exists)
            TextButton(
              key: const ValueKey('flow-delete'),
              onPressed: _delete,
              child: const Text('Delete flow'),
            ),
          const SizedBox(width: 8),
          FilledButton(
            key: const ValueKey('flow-save'),
            onPressed: _saving ? null : _save,
            child: Text(_saving ? 'Saving…' : 'Save flow'),
          ),
        ],
      ),
    ];
  }

  Widget _simulation() {
    final d = _decision;
    return AppSurface(
      elevation: AppSurfaceElevation.canvas,
      padding: const EdgeInsets.all(12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: AppText.muted(
                  'Check the saved flow against the current time and quotas.',
                ),
              ),
              OutlinedButton.icon(
                key: const ValueKey('flow-simulate'),
                onPressed: _simulate,
                icon: const Icon(Icons.play_arrow, size: 18),
                label: const Text('Which agent now?'),
              ),
            ],
          ),
          if (d != null) ...[
            const SizedBox(height: 10),
            Text(
              d.candidates.isEmpty
                  ? 'No agent would review now: the review would wait.'
                  : 'Reviews now with ${d.candidates.first}'
                        '${d.candidates.length > 1 ? ', then ${d.candidates.skip(1).join(', ')} if it runs out of quota' : ''}.',
              key: const ValueKey('flow-simulation-result'),
              style: const TextStyle(fontWeight: FontWeight.w600),
            ),
            const SizedBox(height: 6),
            for (final r in d.rules)
              Padding(
                padding: const EdgeInsets.only(top: 4),
                child: Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Icon(
                      r.matched && r.available
                          ? Icons.check_circle
                          : Icons.cancel_outlined,
                      size: 16,
                      color: r.matched && r.available
                          ? AppColors.success.resolve(context)
                          : AppColors.textMuted.resolve(context),
                    ),
                    const SizedBox(width: 6),
                    Expanded(
                      child: Text('${r.agent}: ${r.reasons.join('; ')}'),
                    ),
                  ],
                ),
              ),
          ],
        ],
      ),
    );
  }
}

/// Editor for one rule: agent, match mode and its conditions.
class FlowRuleCard extends StatelessWidget {
  final int index;
  final FlowRule rule;
  final List<String> agents;
  final ValueChanged<FlowRule> onChanged;
  final VoidCallback onDelete;

  const FlowRuleCard({
    super.key,
    required this.index,
    required this.rule,
    required this.agents,
    required this.onChanged,
    required this.onDelete,
  });

  @override
  Widget build(BuildContext context) {
    return AppSurface(
      padding: const EdgeInsets.all(12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Text(
                '${index + 1}.',
                style: const TextStyle(fontWeight: FontWeight.w700),
              ),
              const SizedBox(width: 8),
              SizedBox(
                width: 180,
                child: DropdownButtonFormField<String>(
                  key: ValueKey('rule-$index-agent'),
                  isExpanded: true,
                  initialValue: agents.contains(rule.agent) ? rule.agent : null,
                  decoration: const InputDecoration(
                    labelText: 'Agent',
                    border: OutlineInputBorder(),
                    isDense: true,
                  ),
                  items: [
                    for (final a in agents)
                      DropdownMenuItem(value: a, child: Text(a)),
                  ],
                  onChanged: (v) =>
                      v == null ? null : onChanged(rule.copyWith(agent: v)),
                ),
              ),
              const SizedBox(width: 12),
              if (!rule.isCatchAll)
                SegmentedButton<String>(
                  key: ValueKey('rule-$index-match'),
                  segments: const [
                    ButtonSegment(value: 'all', label: Text('All')),
                    ButtonSegment(value: 'any', label: Text('Any')),
                  ],
                  selected: {rule.match},
                  onSelectionChanged: (s) =>
                      onChanged(rule.copyWith(match: s.first)),
                ),
              const Spacer(),
              IconButton(
                key: ValueKey('rule-$index-delete'),
                tooltip: 'Remove rule',
                icon: const Icon(Icons.delete_outline),
                onPressed: onDelete,
              ),
            ],
          ),
          const SizedBox(height: 8),
          if (rule.isCatchAll)
            AppText.muted('No conditions: this rule always applies.'),
          // The count in each row's key re-creates the rows when a condition
          // is added or removed (their fields hold initial values), but keeps
          // them, and their focus, while one is edited.
          for (var i = 0; i < rule.schedule.length; i++)
            _ScheduleRow(
              key: ValueKey(
                'rule-$index-schedule-$i-of-${rule.schedule.length}',
              ),
              keyPrefix: 'rule-$index-schedule-$i',
              value: rule.schedule[i],
              onChanged: (s) {
                final list = [...rule.schedule]..[i] = s;
                onChanged(rule.copyWith(schedule: list));
              },
              onDelete: () => onChanged(
                rule.copyWith(schedule: [...rule.schedule]..removeAt(i)),
              ),
            ),
          for (var i = 0; i < rule.quota.length; i++)
            _QuotaRow(
              key: ValueKey('rule-$index-quota-$i-of-${rule.quota.length}'),
              keyPrefix: 'rule-$index-quota-$i',
              value: rule.quota[i],
              agents: agents,
              onChanged: (q) {
                final list = [...rule.quota]..[i] = q;
                onChanged(rule.copyWith(quota: list));
              },
              onDelete: () =>
                  onChanged(rule.copyWith(quota: [...rule.quota]..removeAt(i))),
            ),
          const SizedBox(height: 6),
          Wrap(
            spacing: 8,
            children: [
              TextButton.icon(
                key: ValueKey('rule-$index-add-schedule'),
                onPressed: () => onChanged(
                  rule.copyWith(
                    schedule: [...rule.schedule, const ScheduleCondition()],
                  ),
                ),
                icon: const Icon(Icons.schedule, size: 16),
                label: const Text('Time window'),
              ),
              TextButton.icon(
                key: ValueKey('rule-$index-add-quota'),
                onPressed: () => onChanged(
                  rule.copyWith(
                    quota: [
                      ...rule.quota,
                      QuotaCondition(agent: rule.agent),
                    ],
                  ),
                ),
                icon: const Icon(Icons.speed, size: 16),
                label: const Text('Quota threshold'),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

final _hhmm = RegExp(r'^([01]\d|2[0-3]):[0-5]\d$');

class _ScheduleRow extends StatelessWidget {
  final String keyPrefix;
  final ScheduleCondition value;
  final ValueChanged<ScheduleCondition> onChanged;
  final VoidCallback onDelete;

  const _ScheduleRow({
    super.key,
    required this.keyPrefix,
    required this.value,
    required this.onChanged,
    required this.onDelete,
  });

  Widget _time(String label, String initial, ValueChanged<String> set) {
    return SizedBox(
      width: 90,
      child: TextFormField(
        key: ValueKey('$keyPrefix-$label'),
        initialValue: initial,
        decoration: InputDecoration(
          labelText: label,
          border: const OutlineInputBorder(),
          isDense: true,
        ),
        autovalidateMode: AutovalidateMode.onUserInteraction,
        validator: (v) => _hhmm.hasMatch(v ?? '') ? null : 'HH:MM',
        onChanged: (v) {
          if (_hhmm.hasMatch(v)) set(v);
        },
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.only(top: 6),
      child: Wrap(
        crossAxisAlignment: WrapCrossAlignment.center,
        spacing: 6,
        runSpacing: 6,
        children: [
          const Icon(Icons.schedule, size: 16),
          for (final d in flowWeekdays)
            FilterChip(
              key: ValueKey('$keyPrefix-day-$d'),
              label: Text(d),
              selected: value.days.contains(d),
              onSelected: (on) {
                final days = [...value.days];
                on ? days.add(d) : days.remove(d);
                days.sort(
                  (a, b) => flowWeekdays
                      .indexOf(a)
                      .compareTo(flowWeekdays.indexOf(b)),
                );
                onChanged(value.copyWith(days: days));
              },
            ),
          _time('from', value.from, (v) => onChanged(value.copyWith(from: v))),
          _time('to', value.to, (v) => onChanged(value.copyWith(to: v))),
          SizedBox(
            width: 170,
            child: TextFormField(
              key: ValueKey('$keyPrefix-tz'),
              initialValue: value.tz,
              decoration: const InputDecoration(
                labelText: 'Time zone',
                hintText: 'Europe/Madrid',
                border: OutlineInputBorder(),
                isDense: true,
              ),
              onChanged: (v) => onChanged(value.copyWith(tz: v.trim())),
            ),
          ),
          IconButton(
            key: ValueKey('$keyPrefix-delete'),
            tooltip: 'Remove condition',
            icon: const Icon(Icons.close, size: 16),
            onPressed: onDelete,
          ),
        ],
      ),
    );
  }
}

class _QuotaRow extends StatelessWidget {
  final String keyPrefix;
  final QuotaCondition value;
  final List<String> agents;
  final ValueChanged<QuotaCondition> onChanged;
  final VoidCallback onDelete;

  const _QuotaRow({
    super.key,
    required this.keyPrefix,
    required this.value,
    required this.agents,
    required this.onChanged,
    required this.onDelete,
  });

  @override
  Widget build(BuildContext context) {
    final windows = [
      ...flowQuotaWindows,
      if (!flowQuotaWindows.contains(value.window)) value.window,
    ];
    return Padding(
      padding: const EdgeInsets.only(top: 6),
      child: Wrap(
        crossAxisAlignment: WrapCrossAlignment.center,
        spacing: 6,
        runSpacing: 6,
        children: [
          const Icon(Icons.speed, size: 16),
          SizedBox(
            width: 140,
            child: DropdownButtonFormField<String>(
              key: ValueKey('$keyPrefix-agent'),
              isExpanded: true,
              initialValue: agents.contains(value.agent) ? value.agent : null,
              decoration: const InputDecoration(
                labelText: 'Agent',
                border: OutlineInputBorder(),
                isDense: true,
              ),
              items: [
                for (final a in agents)
                  DropdownMenuItem(value: a, child: Text(a)),
              ],
              onChanged: (v) =>
                  v == null ? null : onChanged(value.copyWith(agent: v)),
            ),
          ),
          SizedBox(
            width: 130,
            child: DropdownButtonFormField<String>(
              key: ValueKey('$keyPrefix-window'),
              isExpanded: true,
              initialValue: value.window,
              decoration: const InputDecoration(
                labelText: 'Window',
                border: OutlineInputBorder(),
                isDense: true,
              ),
              items: [
                for (final w in windows)
                  DropdownMenuItem(value: w, child: Text(w)),
              ],
              onChanged: (v) =>
                  v == null ? null : onChanged(value.copyWith(window: v)),
            ),
          ),
          SizedBox(
            width: 110,
            child: DropdownButtonFormField<String>(
              key: ValueKey('$keyPrefix-op'),
              isExpanded: true,
              initialValue: value.op,
              decoration: const InputDecoration(
                labelText: 'Used',
                border: OutlineInputBorder(),
                isDense: true,
              ),
              items: const [
                DropdownMenuItem(value: 'below', child: Text('below')),
                DropdownMenuItem(value: 'above', child: Text('above')),
              ],
              onChanged: (v) =>
                  v == null ? null : onChanged(value.copyWith(op: v)),
            ),
          ),
          SizedBox(
            width: 80,
            child: TextFormField(
              key: ValueKey('$keyPrefix-percent'),
              initialValue: '${value.percent.round()}',
              decoration: const InputDecoration(
                labelText: '%',
                border: OutlineInputBorder(),
                isDense: true,
              ),
              keyboardType: TextInputType.number,
              inputFormatters: [FilteringTextInputFormatter.digitsOnly],
              onChanged: (v) {
                final n = int.tryParse(v);
                if (n != null) {
                  onChanged(
                    value.copyWith(percent: n.clamp(0, 100).toDouble()),
                  );
                }
              },
            ),
          ),
          IconButton(
            key: ValueKey('$keyPrefix-delete'),
            tooltip: 'Remove condition',
            icon: const Icon(Icons.close, size: 16),
            onPressed: onDelete,
          ),
        ],
      ),
    );
  }
}
