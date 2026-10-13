import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/flows/canvas/flow_canvas.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/editor/inspector/agent_inspector.dart';
import 'package:heimdallm/features/flows/editor/inspector/condition_inspector.dart';
import 'package:heimdallm/features/flows/editor/inspector/flow_inspector.dart';
import 'package:heimdallm/features/flows/editor/palette.dart';
import 'package:heimdallm/features/flows/editor/test_panel.dart';
import 'package:heimdallm/features/flows/flows_list_screen.dart';
import 'package:heimdallm/shared/design_system/color_resolver.dart';
import 'package:heimdallm/shared/design_system/components/components.dart';
import 'package:heimdallm/shared/design_system/tokens.dart';
import 'package:heimdallm/shared/widgets/toast.dart';

final flowEditorDirtyProvider = Provider.autoDispose<bool>(
  (ref) => ref.watch(flowEditorProvider.select((s) => s.dirty)),
);

Future<bool> confirmDiscardFlowChanges(BuildContext context) async {
  final ok = await showDialog<bool>(
    context: context,
    builder: (ctx) => AlertDialog(
      title: const Text('Discard unsaved changes?'),
      content: const Text('The flow has changes that are not saved.'),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(ctx, false),
          child: const Text('Keep editing'),
        ),
        FilledButton(
          onPressed: () => Navigator.pop(ctx, true),
          child: const Text('Discard'),
        ),
      ],
    ),
  );
  return ok ?? false;
}

/// go_router `onExit` for `/flows/:id`.
Future<bool> flowEditorOnExit(BuildContext context) async {
  final dirty = ProviderScope.containerOf(
    context,
    listen: false,
  ).read(flowEditorDirtyProvider);
  if (!dirty) return true;
  return confirmDiscardFlowChanges(context);
}

/// Where a flow is selected: globally, and by each org and repo override.
List<String> flowUsedBy(String flowId, String globalFlow, AppConfig? config) {
  final orgs = config?.orgConfigs.entries.toList() ?? const [];
  final repos = config?.repoConfigs.entries.toList() ?? const [];
  return [
    if (globalFlow == flowId) 'Global',
    for (final e in orgs)
      if (e.value.flow == flowId) 'org ${e.key}',
    for (final e in repos)
      if (e.value.flow == flowId) 'repo ${e.key}',
  ]..sort((a, b) => a == 'Global' ? -1 : (b == 'Global' ? 1 : a.compareTo(b)));
}

/// The node editor for one flow: palette on the left, the graph canvas in
/// the centre and the inspector on the right, under a bar with the flow's
/// actions.
class FlowEditorScreen extends ConsumerStatefulWidget {
  final String flowId;
  const FlowEditorScreen({super.key, required this.flowId});

  @override
  ConsumerState<FlowEditorScreen> createState() => _FlowEditorScreenState();
}

class _FlowEditorScreenState extends ConsumerState<FlowEditorScreen> {
  FlowListing? _listing;
  String? _loadError;
  bool _saving = false;
  bool _testOpen = false;

  /// The daemon's findings from the last rejected save, and the flow they
  /// were about: a step's findings show until that step changes.
  List<FlowIssue> _serverIssues = const [];
  ReviewFlow _issuesAgainst = const ReviewFlow();

  bool get _isDefault =>
      _listing != null && widget.flowId == _listing!.defaultId;

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void didUpdateWidget(FlowEditorScreen old) {
    super.didUpdateWidget(old);
    if (old.flowId != widget.flowId) {
      setState(() {
        _listing = null;
        _serverIssues = const [];
        _testOpen = false;
      });
      _load();
    }
  }

  Future<void> _load() async {
    setState(() => _loadError = null);
    try {
      final listing = await ref.read(apiClientProvider).fetchFlows();
      if (!mounted) return;
      ref
          .read(flowEditorProvider.notifier)
          .load(
            listing.flows[widget.flowId] ?? ReviewFlow(name: widget.flowId),
          );
      setState(() => _listing = listing);
    } on ApiException catch (e) {
      if (mounted) setState(() => _loadError = e.message);
    }
  }

  Future<void> _reloadListingOnly() async {
    final listing = await ref.read(apiClientProvider).fetchFlows();
    if (mounted) setState(() => _listing = listing);
  }

  void _afterServerChange(Map<String, dynamic> json) {
    ref.read(configNotifierProvider.notifier).updateFromServer(json);
    ref.invalidate(flowsProvider);
  }

  Future<void> _save() async {
    final state = ref.read(flowEditorProvider);
    final ctl = ref.read(flowEditorProvider.notifier);
    if (!state.check.ok) {
      final first = state.check.errors.first;
      showToast(context, first.message, isError: true);
      ctl.select(
        first.node.isEmpty ? const NoSelection() : NodeSelection(first.node),
      );
      return;
    }
    setState(() => _saving = true);
    try {
      final json = await ref
          .read(apiClientProvider)
          .putFlow(widget.flowId, state.flow);
      if (!mounted) return;
      _afterServerChange(json);
      ctl.markSaved();
      setState(() => _serverIssues = const []);
      await _reloadListingOnly();
      if (mounted) showToast(context, 'Flow saved');
    } on FlowValidationException catch (e) {
      if (!mounted) return;
      setState(() {
        _serverIssues = e.issues;
        _issuesAgainst = state.flow;
      });
      final pinned = e.issues.where((i) => i.node.isNotEmpty).firstOrNull;
      if (pinned != null && state.flow.nodes.containsKey(pinned.node)) {
        ctl.select(NodeSelection(pinned.node));
      }
      showToast(context, e.message, isError: true);
    } on ApiException catch (e) {
      if (mounted) showToast(context, e.message, isError: true);
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
          "Repositories and organizations using it go back to their parent's flow.",
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
      if (!mounted) return;
      _afterServerChange(json);
      // Nothing left to lose: leave without the unsaved-changes prompt.
      ref.read(flowEditorProvider.notifier).markSaved();
      context.go('/flows');
    } on ApiException catch (e) {
      if (mounted) showToast(context, e.message, isError: true);
    }
  }

  Future<void> _useGlobally(bool use) async {
    try {
      final json = await ref.read(apiClientProvider).patchConfig({
        'ai': {'flow': use ? widget.flowId : ''},
      });
      if (!mounted) return;
      _afterServerChange(json);
      await _reloadListingOnly();
    } on ApiException catch (e) {
      if (mounted) showToast(context, e.message, isError: true);
    }
  }

  Future<void> _duplicate() async {
    final flow = ref.read(flowEditorProvider).flow;
    final id = await askNewFlowId(context, _listing!);
    if (id == null || !mounted) return;
    try {
      final json = await ref
          .read(apiClientProvider)
          .putFlow(id, flow.copyWith(name: '${flow.name} (copy)'));
      if (!mounted) return;
      _afterServerChange(json);
      context.go('/flows/$id');
    } on ApiException catch (e) {
      if (mounted) showToast(context, e.message, isError: true);
    }
  }

  void _addAtCentre(FlowNodeType type) {
    final count = ref.read(flowEditorProvider).flow.nodes.length;
    ref
        .read(flowEditorProvider.notifier)
        .addNode(
          type,
          Offset(400 + 30.0 * (count % 8), 400 + 30.0 * (count % 8)),
        );
  }

  @override
  Widget build(BuildContext context) {
    // Watched before the listing arrives too: the provider is autoDispose,
    // and _load fills it while the spinner shows.
    final state = ref.watch(flowEditorProvider);
    final listing = _listing;
    if (listing == null) {
      return Scaffold(
        body: Center(
          child: _loadError == null
              ? const CircularProgressIndicator()
              : Column(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    AppText('Could not load the flow: $_loadError'),
                    const SizedBox(height: 12),
                    OutlinedButton(
                      onPressed: _load,
                      child: const Text('Retry'),
                    ),
                  ],
                ),
        ),
      );
    }
    final ctl = ref.read(flowEditorProvider.notifier);
    final config = ref.watch(configNotifierProvider).value;
    final readOnly = _isDefault;
    final canEdit = !readOnly && state.draft == null;

    // A step's daemon findings hold until that step changes.
    final liveServer = [
      for (final i in _serverIssues)
        if (i.node.isEmpty ||
            (state.flow.nodes.containsKey(i.node) &&
                identical(
                  state.flow.nodes[i.node],
                  _issuesAgainst.nodes[i.node],
                )))
          i,
    ];
    final issuesFor = <String, List<FlowIssue>>{};
    for (final i in [...state.check.errors, ...liveServer]) {
      if (i.node.isNotEmpty) issuesFor.putIfAbsent(i.node, () => []).add(i);
    }

    final selection = state.selection;
    final selected = selection is NodeSelection
        ? state.flow.nodes[selection.id]
        : null;
    final Widget inspector = switch (selected) {
      // Keyed by step so switching steps never reuses another's fields.
      final FlowNode n when n.isCondition => ConditionInspector(
        key: ValueKey('inspector-${n.id}'),
        node: n,
        agents: listing.agents,
        issues: issuesFor[n.id] ?? const [],
      ),
      final FlowNode n => AgentInspector(
        key: ValueKey('inspector-${n.id}'),
        node: n,
        agents: listing.agents,
        issues: issuesFor[n.id] ?? const [],
      ),
      null => FlowInspector(
        flowId: widget.flowId,
        usedBy: flowUsedBy(widget.flowId, listing.selected, config),
        readOnly: readOnly,
        issues: [
          for (final i in liveServer)
            if (i.node.isEmpty) i,
        ],
      ),
    };
    // While a draft is shown, the inspector would edit the flow under it.
    final inspectorPane = IgnorePointer(
      ignoring: state.draft != null,
      child: inspector,
    );

    void undo() {
      if (canEdit) ctl.undo();
    }

    void redo() {
      if (canEdit) ctl.redo();
    }

    return CallbackShortcuts(
      bindings: {
        const SingleActivator(LogicalKeyboardKey.keyZ, meta: true): undo,
        const SingleActivator(LogicalKeyboardKey.keyZ, control: true): undo,
        const SingleActivator(LogicalKeyboardKey.keyZ, meta: true, shift: true):
            redo,
        const SingleActivator(
          LogicalKeyboardKey.keyZ,
          control: true,
          shift: true,
        ): redo,
      },
      child: Scaffold(
        body: LayoutBuilder(
          builder: (context, c) {
            final compact = c.maxWidth < 900;
            final border = resolveAppColor(context, AppColors.border);
            final panel = AppColors.surface.resolve(context);
            final canvas = Stack(
              children: [
                Positioned.fill(
                  child: FlowCanvas(
                    readOnly: readOnly,
                    errorNodes: {
                      for (final i in liveServer)
                        if (i.node.isNotEmpty) i.node,
                    },
                    onDropFromPalette: readOnly
                        ? null
                        : (t, at) => ctl.addNode(t, at),
                  ),
                ),
                if (_testOpen)
                  Positioned(
                    left: 16,
                    bottom: 16,
                    width: 340,
                    child: _FloatingPanel(
                      maxHeight: c.maxHeight - 120,
                      child: FlowTestPanel(
                        onClose: () {
                          ctl.setSimulation(null);
                          setState(() => _testOpen = false);
                        },
                      ),
                    ),
                  ),
                // Task 12 adds the assistant button and panel here.
              ],
            );
            final Widget body;
            if (compact) {
              body = Column(
                children: [
                  Expanded(child: canvas),
                  if (!readOnly)
                    Container(
                      height: 280,
                      decoration: BoxDecoration(
                        color: panel,
                        border: Border(top: BorderSide(color: border)),
                      ),
                      child: SingleChildScrollView(
                        padding: const EdgeInsets.all(16),
                        child: inspectorPane,
                      ),
                    ),
                ],
              );
            } else {
              body = Row(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  if (!readOnly)
                    Container(
                      width: 240,
                      decoration: BoxDecoration(
                        color: panel,
                        border: Border(right: BorderSide(color: border)),
                      ),
                      child: FlowPalette(onAdd: _addAtCentre),
                    ),
                  Expanded(child: canvas),
                  Container(
                    width: 340,
                    decoration: BoxDecoration(
                      color: panel,
                      border: Border(left: BorderSide(color: border)),
                    ),
                    child: SingleChildScrollView(
                      padding: const EdgeInsets.all(20),
                      child: inspectorPane,
                    ),
                  ),
                ],
              );
            }
            return Column(
              children: [
                _TopBar(
                  compact: compact,
                  flowId: widget.flowId,
                  name: state.flow.name,
                  dirty: state.dirty,
                  readOnly: readOnly,
                  usedGlobally: listing.selected == widget.flowId,
                  canUndo: canEdit && state.canUndo,
                  canRedo: canEdit && state.canRedo,
                  canArrange: canEdit && state.flow.nodes.isNotEmpty,
                  canDelete: listing.flows.containsKey(widget.flowId),
                  canSave: !_saving && state.draft == null,
                  saving: _saving,
                  testOpen: _testOpen,
                  onBack: () => context.go('/flows'),
                  onUseGlobally: _useGlobally,
                  onUndo: ctl.undo,
                  onRedo: ctl.redo,
                  onArrange: ctl.arrange,
                  onToggleTest: () {
                    if (_testOpen) ctl.setSimulation(null);
                    setState(() => _testOpen = !_testOpen);
                  },
                  onDelete: _delete,
                  onSave: _save,
                  onDuplicate: _duplicate,
                ),
                Divider(height: 1, thickness: 1, color: border),
                Expanded(child: body),
              ],
            );
          },
        ),
      ),
    );
  }
}

/// A card floating over the canvas, scrolling when taller than [maxHeight].
class _FloatingPanel extends StatelessWidget {
  final double maxHeight;
  final Widget child;
  const _FloatingPanel({required this.maxHeight, required this.child});

  @override
  Widget build(BuildContext context) => DecoratedBox(
    decoration: BoxDecoration(
      borderRadius: BorderRadius.circular(8),
      boxShadow: [
        BoxShadow(
          color: Colors.black.withValues(alpha: .18),
          blurRadius: 16,
          offset: const Offset(0, 4),
        ),
      ],
    ),
    child: AppSurface(
      elevation: AppSurfaceElevation.surface,
      child: ConstrainedBox(
        constraints: BoxConstraints(maxHeight: maxHeight.clamp(160, 640)),
        child: SingleChildScrollView(
          padding: const EdgeInsets.all(14),
          child: child,
        ),
      ),
    ),
  );
}

/// The editor's bar: back, the flow's name and state, and its actions.
class _TopBar extends StatelessWidget {
  final bool compact;
  final String flowId;
  final String name;
  final bool dirty;
  final bool readOnly;
  final bool usedGlobally;
  final bool canUndo;
  final bool canRedo;
  final bool canArrange;
  final bool canDelete;
  final bool canSave;
  final bool saving;
  final bool testOpen;
  final VoidCallback onBack;
  final ValueChanged<bool> onUseGlobally;
  final VoidCallback onUndo;
  final VoidCallback onRedo;
  final VoidCallback onArrange;
  final VoidCallback onToggleTest;
  final VoidCallback onDelete;
  final VoidCallback onSave;
  final VoidCallback onDuplicate;

  const _TopBar({
    required this.compact,
    required this.flowId,
    required this.name,
    required this.dirty,
    required this.readOnly,
    required this.usedGlobally,
    required this.canUndo,
    required this.canRedo,
    required this.canArrange,
    required this.canDelete,
    required this.canSave,
    required this.saving,
    required this.testOpen,
    required this.onBack,
    required this.onUseGlobally,
    required this.onUndo,
    required this.onRedo,
    required this.onArrange,
    required this.onToggleTest,
    required this.onDelete,
    required this.onSave,
    required this.onDuplicate,
  });

  @override
  Widget build(BuildContext context) {
    final muted = resolveAppColor(context, AppColors.textMuted);
    final warning = resolveAppColor(context, AppColors.warning);
    final border = resolveAppColor(context, AppColors.border);
    final danger = resolveAppColor(context, AppColors.danger);
    Widget divider() => Container(
      width: 1,
      height: 24,
      margin: const EdgeInsets.symmetric(horizontal: 8),
      color: border,
    );
    final test = testOpen
        ? FilledButton.tonalIcon(
            key: const ValueKey('flow-test'),
            onPressed: onToggleTest,
            icon: const Icon(Icons.play_arrow, size: 18),
            label: const Text('Test'),
          )
        : OutlinedButton.icon(
            key: const ValueKey('flow-test'),
            onPressed: onToggleTest,
            icon: const Icon(Icons.play_arrow, size: 18),
            label: const Text('Test'),
          );
    return Padding(
      padding: const EdgeInsets.fromLTRB(8, 8, 16, 8),
      child: Row(
        children: [
          IconButton(
            key: const ValueKey('flow-back'),
            tooltip: 'All flows',
            icon: const Icon(Icons.arrow_back),
            onPressed: onBack,
          ),
          const SizedBox(width: 4),
          Expanded(
            child: Row(
              children: [
                Flexible(
                  child: Text(
                    name.isEmpty ? flowId : name,
                    overflow: TextOverflow.ellipsis,
                    style: Theme.of(context).textTheme.titleMedium?.copyWith(
                      fontWeight: FontWeight.w600,
                    ),
                  ),
                ),
                if (!compact && name.isNotEmpty && name != flowId) ...[
                  const SizedBox(width: 8),
                  AppText.mono(flowId, color: muted),
                ],
                const SizedBox(width: 10),
                if (readOnly)
                  AppBadge(
                    label: 'Read-only',
                    foreground: muted,
                    background: AppColors.surfaceRaised.resolve(context),
                    border: border,
                  )
                else if (dirty)
                  AppBadge(
                    key: const ValueKey('flow-unsaved'),
                    label: 'Unsaved',
                    foreground: warning,
                    background: warning.withValues(alpha: .12),
                    border: warning.withValues(alpha: .35),
                  ),
              ],
            ),
          ),
          if (!readOnly) ...[
            Tooltip(
              message:
                  'Review with this flow wherever no org or repo picks '
                  'another',
              child: Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  if (!compact)
                    Text(
                      'Use globally',
                      style: Theme.of(context).textTheme.bodyMedium,
                    ),
                  Switch(
                    key: const ValueKey('flow-use-globally'),
                    value: usedGlobally,
                    onChanged: onUseGlobally,
                  ),
                ],
              ),
            ),
            divider(),
            AppIconButton(
              key: const ValueKey('flow-undo'),
              tooltip: 'Undo',
              icon: Icons.undo,
              onPressed: canUndo ? onUndo : null,
            ),
            AppIconButton(
              key: const ValueKey('flow-redo'),
              tooltip: 'Redo',
              icon: Icons.redo,
              onPressed: canRedo ? onRedo : null,
            ),
            AppIconButton(
              key: const ValueKey('flow-arrange'),
              tooltip: 'Arrange steps',
              icon: Icons.auto_fix_high,
              onPressed: canArrange ? onArrange : null,
            ),
            divider(),
          ],
          test,
          const SizedBox(width: 8),
          if (readOnly)
            FilledButton.icon(
              key: const ValueKey('flow-duplicate'),
              onPressed: onDuplicate,
              icon: const Icon(Icons.copy, size: 18),
              label: const Text('Duplicate as a new flow'),
            )
          else ...[
            IconButton(
              key: const ValueKey('flow-delete'),
              tooltip: 'Delete flow',
              color: danger,
              visualDensity: VisualDensity.compact,
              icon: const Icon(Icons.delete_outline, size: 20),
              onPressed: canDelete ? onDelete : null,
            ),
            const SizedBox(width: 8),
            FilledButton.icon(
              key: const ValueKey('flow-save'),
              onPressed: canSave ? onSave : null,
              icon: saving
                  ? const SizedBox.square(
                      dimension: 16,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    )
                  : Icon(dirty ? Icons.save_outlined : Icons.check, size: 18),
              label: Text(dirty ? 'Save' : 'Saved'),
            ),
          ],
        ],
      ),
    );
  }
}
