import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/graph/flow_diff.dart';
import 'package:heimdallm/shared/design_system/color_resolver.dart';
import 'package:heimdallm/shared/design_system/components/components.dart';
import 'package:heimdallm/shared/design_system/tokens.dart';
import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';

const kAssistAgentPrefKey = 'flows.assist.agent';

/// Describe a flow (or a change) in words; an installed agent drafts it.
/// The draft is drawn on the canvas and only applied on Accept.
class AssistPanel extends ConsumerStatefulWidget {
  final List<FlowAgent> agents;
  final String defaultAgent;
  final VoidCallback onClose;
  const AssistPanel({
    super.key,
    required this.agents,
    required this.defaultAgent,
    required this.onClose,
  });

  @override
  ConsumerState<AssistPanel> createState() => _AssistPanelState();
}

class _AssistPanelState extends ConsumerState<AssistPanel> {
  final _input = TextEditingController();
  String? _agent;
  bool _restored = false;

  /// The remembered agent, and whether the operator picked one here: either
  /// wins over the daemon's primary arriving later.
  String? _stored;
  bool _picked = false;

  /// The open request: closing it cancels the request. While it is open the
  /// editor is locked (`FlowEditorState.drafting`); the screen resets that
  /// when it loads another flow.
  http.Client? _inflight;

  /// Bumped by every run and cancel, so a stale answer is ignored.
  int _generation = 0;
  Timer? _ticker;
  int _elapsed = 0;
  String? _error;
  List<String> _errorDetails = const [];

  List<FlowAgent> get _runnable =>
      widget.agents.where((a) => a.installed && a.canDraft).toList();

  @override
  void initState() {
    super.initState();
    _restoreAgent();
  }

  Future<void> _restoreAgent() async {
    try {
      _stored = (await SharedPreferences.getInstance()).getString(
        kAssistAgentPrefKey,
      );
    } catch (e) {
      debugPrint('assist panel: could not read the last agent: $e');
    }
    if (!mounted) return;
    setState(() {
      _agent = _preferred();
      _restored = true;
    });
  }

  /// The remembered agent if still runnable, else the daemon's primary, else
  /// the first runnable one.
  String? _preferred() {
    final ids = _runnable.map((a) => a.id).toList();
    if (ids.contains(_stored)) return _stored;
    if (ids.contains(widget.defaultAgent)) return widget.defaultAgent;
    return ids.isEmpty ? null : ids.first;
  }

  @override
  void didUpdateWidget(AssistPanel old) {
    super.didUpdateWidget(old);
    // The config (and with it the primary agent) may load after the panel.
    if (_restored && !_picked) {
      final next = _preferred();
      if (next != _agent) setState(() => _agent = next);
    }
  }

  Future<void> _setAgent(String? id) async {
    setState(() {
      _agent = id;
      _picked = true;
    });
    if (id == null) return;
    try {
      await (await SharedPreferences.getInstance()).setString(
        kAssistAgentPrefKey,
        id,
      );
    } catch (e) {
      debugPrint('assist panel: could not remember the agent: $e');
    }
  }

  @override
  void dispose() {
    _ticker?.cancel();
    _inflight?.close();
    _input.dispose();
    super.dispose();
  }

  void _stopWaiting() {
    _ticker?.cancel();
    _ticker = null;
    _inflight = null;
  }

  Future<void> _run() async {
    final agent = _agent;
    final text = _input.text.trim();
    if (agent == null || text.isEmpty || _inflight != null) return;
    final state = ref.read(flowEditorProvider);
    // Refining works on the draft, not on the flow under it.
    final current = state.draft?.flow ?? state.flow;
    final client = http.Client();
    final generation = ++_generation;
    final ticker = Timer.periodic(const Duration(seconds: 1), (_) {
      if (mounted) setState(() => _elapsed++);
    });
    setState(() {
      _inflight = client;
      _ticker = ticker;
      _elapsed = 0;
      _error = null;
      _errorDetails = const [];
    });
    ref.read(flowEditorProvider.notifier).setDrafting(true);
    String? error;
    var details = const <String>[];
    try {
      final r = await ref
          .read(apiClientProvider)
          .assistFlow(
            agent: agent,
            instruction: text,
            current: current.nodes.isEmpty ? null : current,
            client: client,
          );
      if (!mounted || generation != _generation) return;
      ref.read(flowEditorProvider.notifier).showDraft(r);
      _input.clear();
    } on FlowValidationException catch (e) {
      error = e.message;
      details = [
        for (final i in e.issues)
          i.node.isEmpty ? i.message : '${i.node}: ${i.message}',
      ];
    } on ApiException catch (e) {
      error = e.message;
    } on http.ClientException catch (e) {
      error = 'Could not reach the daemon: ${e.message}';
    } catch (e) {
      error = 'Could not draft the flow: $e';
    } finally {
      client.close();
      // A cancelled run already reset the panel; its outcome is dropped.
      if (mounted && generation == _generation) {
        setState(() {
          _stopWaiting();
          _error = error;
          _errorDetails = details;
        });
        ref.read(flowEditorProvider.notifier).setDrafting(false);
      }
    }
  }

  void _cancel() {
    _generation++;
    _inflight?.close();
    setState(_stopWaiting);
    ref.read(flowEditorProvider.notifier).setDrafting(false);
  }

  void _close() {
    if (_inflight != null) _cancel();
    // A draft left on the canvas would keep the editor read-only.
    ref.read(flowEditorProvider.notifier).discardDraft();
    widget.onClose();
  }

  String get _elapsedLabel =>
      '${_elapsed ~/ 60}:${(_elapsed % 60).toString().padLeft(2, '0')}';

  @override
  Widget build(BuildContext context) {
    final draft = ref.watch(flowEditorProvider.select((s) => s.draft));
    final ctl = ref.read(flowEditorProvider.notifier);
    final accent = resolveAppColor(context, AppColors.accent);
    final danger = resolveAppColor(context, AppColors.danger);
    final busy = _inflight != null;
    final runnable = _runnable;
    final small = Theme.of(context).textTheme.bodySmall;

    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      mainAxisSize: MainAxisSize.min,
      children: [
        Row(
          children: [
            Icon(Icons.auto_awesome, size: 18, color: accent),
            const SizedBox(width: 8),
            Expanded(child: AppText.sectionTitle('Adjust with AI')),
            IconButton(
              key: const ValueKey('assist-close'),
              tooltip: draft == null
                  ? 'Back to the inspector'
                  : 'Discard the draft and go back to the inspector',
              visualDensity: VisualDensity.compact,
              icon: const Icon(Icons.close, size: 18),
              onPressed: _close,
            ),
          ],
        ),
        const SizedBox(height: 4),
        AppText.muted(
          'Describe the flow you want, or a change to it. The agent drafts '
          'it on the canvas; nothing changes until you accept.',
        ),
        const SizedBox(height: 12),
        if (runnable.isEmpty)
          AppText.muted(
            'No installed CLI agent can draft flows. Install one from the '
            'Agents page.',
          )
        else if (_restored)
          DropdownButtonFormField<String>(
            key: const ValueKey('assist-agent'),
            initialValue: _agent,
            isDense: true,
            decoration: const InputDecoration(
              labelText: 'Agent that drafts the flow',
              isDense: true,
              border: OutlineInputBorder(),
            ),
            items: [
              for (final a in runnable)
                DropdownMenuItem(value: a.id, child: Text(a.id)),
            ],
            onChanged: busy ? null : _setAgent,
          ),
        if (draft != null) ...[
          const SizedBox(height: 12),
          _DraftReview(
            draft: draft,
            busy: busy,
            onAccept: ctl.acceptDraft,
            onDiscard: ctl.discardDraft,
          ),
        ],
        const SizedBox(height: 12),
        TextField(
          key: const ValueKey('assist-input'),
          controller: _input,
          enabled: !busy,
          minLines: 2,
          maxLines: 6,
          maxLength: 4000,
          decoration: InputDecoration(
            labelText: draft == null ? 'What should the flow do?' : 'Refine',
            alignLabelWithHint: true,
            border: const OutlineInputBorder(),
            hintText: draft == null
                ? 'e.g. At night use Codex; if Claude is above 80% of its '
                      'weekly quota, use Gemini'
                : 'e.g. and use Copilot at weekends',
          ),
        ),
        if (_error != null) ...[
          const SizedBox(height: 4),
          Container(
            key: const ValueKey('assist-error'),
            padding: const EdgeInsets.all(10),
            decoration: BoxDecoration(
              color: danger.withValues(alpha: .08),
              border: Border.all(color: danger.withValues(alpha: .35)),
              borderRadius: BorderRadius.circular(6),
            ),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(_error!, style: small?.copyWith(color: danger)),
                for (final d in _errorDetails)
                  Text('• $d', style: small?.copyWith(color: danger)),
              ],
            ),
          ),
        ],
        const SizedBox(height: 8),
        if (busy)
          _Waiting(
            agent: _agent ?? '',
            elapsed: _elapsedLabel,
            onCancel: _cancel,
          )
        else
          Align(
            alignment: Alignment.centerRight,
            child: FilledButton.icon(
              key: const ValueKey('assist-run'),
              onPressed: runnable.isEmpty || _agent == null ? null : _run,
              icon: const Icon(Icons.auto_awesome, size: 18),
              label: Text(draft == null ? 'Draft' : 'Refine draft'),
            ),
          ),
      ],
    );
  }
}

/// Progress while the agent works: it can take minutes.
class _Waiting extends StatelessWidget {
  final String agent;
  final String elapsed;
  final VoidCallback onCancel;
  const _Waiting({
    required this.agent,
    required this.elapsed,
    required this.onCancel,
  });

  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      const LinearProgressIndicator(minHeight: 3),
      const SizedBox(height: 8),
      Row(
        children: [
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                AppText('$agent is drafting… $elapsed'),
                AppText.muted('This can take a few minutes.'),
              ],
            ),
          ),
          TextButton(
            key: const ValueKey('assist-cancel'),
            onPressed: onCancel,
            child: const Text('Cancel'),
          ),
        ],
      ),
    ],
  );
}

/// The agent's plan, what the draft changes, and Accept / Discard.
class _DraftReview extends StatelessWidget {
  final FlowDraft draft;
  final bool busy;
  final VoidCallback onAccept;
  final VoidCallback onDiscard;
  const _DraftReview({
    required this.draft,
    required this.busy,
    required this.onAccept,
    required this.onDiscard,
  });

  @override
  Widget build(BuildContext context) {
    final border = resolveAppColor(context, AppColors.border);
    final muted = resolveAppColor(context, AppColors.textMuted);
    final warning = resolveAppColor(context, AppColors.warning);
    final counts = <DiffKind, int>{};
    for (final k in draft.diff.nodes.values) {
      counts[k] = (counts[k] ?? 0) + 1;
    }
    Color colour(DiffKind k) => resolveAppColor(context, switch (k) {
      DiffKind.added => AppColors.success,
      DiffKind.changed => AppColors.warning,
      DiffKind.removed => AppColors.danger,
    });
    return Container(
      key: const ValueKey('assist-draft'),
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: AppColors.surfaceRaised.resolve(context),
        border: Border.all(color: border),
        borderRadius: BorderRadius.circular(8),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          AppText.label('Draft plan', color: muted),
          const SizedBox(height: 6),
          AppText(draft.plan.isEmpty ? 'The agent gave no plan.' : draft.plan),
          const SizedBox(height: 10),
          Wrap(
            spacing: 6,
            runSpacing: 6,
            children: [
              if (counts.isEmpty)
                AppBadge(
                  label: 'No changes',
                  foreground: muted,
                  background: Colors.transparent,
                  border: border,
                ),
              for (final k in DiffKind.values)
                if (counts[k] != null)
                  AppBadge(
                    label: '${counts[k]} ${k.name}',
                    foreground: colour(k),
                    background: colour(k).withValues(alpha: .12),
                    border: colour(k).withValues(alpha: .35),
                  ),
            ],
          ),
          for (final w in draft.warnings) ...[
            const SizedBox(height: 6),
            Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Icon(Icons.warning_amber_rounded, size: 16, color: warning),
                const SizedBox(width: 6),
                Expanded(child: AppText.muted(w)),
              ],
            ),
          ],
          const SizedBox(height: 10),
          // Wraps in a narrow column rather than overflowing it.
          Wrap(
            alignment: WrapAlignment.end,
            spacing: 8,
            runSpacing: 8,
            children: [
              OutlinedButton(
                key: const ValueKey('assist-discard'),
                onPressed: busy ? null : onDiscard,
                child: const Text('Discard'),
              ),
              FilledButton.icon(
                key: const ValueKey('assist-accept'),
                onPressed: busy ? null : onAccept,
                icon: const Icon(Icons.check, size: 18),
                label: const Text('Accept'),
              ),
            ],
          ),
        ],
      ),
    );
  }
}
