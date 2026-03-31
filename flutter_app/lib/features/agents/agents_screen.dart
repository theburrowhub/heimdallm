import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/models/agent.dart';
import '../../shared/widgets/toast.dart';
import '../dashboard/dashboard_providers.dart';

// ── Provider ─────────────────────────────────────────────────────────────────

final agentsProvider = FutureProvider<List<ReviewPrompt>>((ref) async {
  final api = ref.watch(apiClientProvider);
  final raw = await api.fetchAgents();
  return raw.map(ReviewPrompt.fromJson).toList();
});

// ── Screen ───────────────────────────────────────────────────────────────────

class AgentsScreen extends ConsumerWidget {
  const AgentsScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final promptsAsync = ref.watch(agentsProvider);

    return promptsAsync.when(
      loading: () => const Center(child: CircularProgressIndicator()),
      error: (e, _) => Center(child: Text('Error: $e')),
      data: (prompts) => _PromptsView(prompts: prompts),
    );
  }
}

// ── Main view ─────────────────────────────────────────────────────────────────

class _PromptsView extends ConsumerWidget {
  final List<ReviewPrompt> prompts;
  const _PromptsView({required this.prompts});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final activePrompt = prompts.where((p) => p.isDefault).firstOrNull;

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        // Active prompt banner
        if (activePrompt != null)
          _ActiveBanner(prompt: activePrompt)
        else
          const _InfoBanner('No active prompt. Select one to customise review behaviour.'),

        // Section: Presets
        _SectionHeader(
          title: 'Presets',
          trailing: TextButton.icon(
            icon: const Icon(Icons.add, size: 16),
            label: const Text('Custom'),
            onPressed: () => _openEditor(context, ref, null),
          ),
        ),
        SizedBox(
          height: 130,
          child: ListView(
            scrollDirection: Axis.horizontal,
            padding: const EdgeInsets.symmetric(horizontal: 16),
            children: ReviewPrompt.presets.map((preset) {
              final already = prompts.any((p) => p.id == preset.id);
              return _PresetCard(
                preset: preset,
                added: already,
                onAdd: already ? null : () => _addPreset(context, ref, preset),
                onActivate: already
                    ? () => _setDefault(context, ref, prompts.firstWhere((p) => p.id == preset.id))
                    : null,
              );
            }).toList(),
          ),
        ),

        const SizedBox(height: 8),

        // Section: My Prompts
        if (prompts.isNotEmpty) ...[
          const _SectionHeader(title: 'My Prompts'),
          Expanded(
            child: ListView.builder(
              padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 4),
              itemCount: prompts.length,
              itemBuilder: (_, i) => _PromptTile(
                prompt: prompts[i],
                onEdit: () => _openEditor(context, ref, prompts[i]),
                onDelete: () => _delete(context, ref, prompts[i]),
                onActivate: () => _setDefault(context, ref, prompts[i]),
              ),
            ),
          ),
        ] else
          const Expanded(
            child: Center(
              child: Text('Add a preset or create a custom prompt.',
                  style: TextStyle(color: Colors.grey)),
            ),
          ),
      ],
    );
  }

  Future<void> _addPreset(BuildContext context, WidgetRef ref, PresetDef preset) async {
    final p = ReviewPrompt.fromPreset(preset);
    try {
      await ref.read(apiClientProvider).upsertAgent(p.toJson());
      ref.invalidate(agentsProvider);
    } catch (e) {
      if (context.mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _setDefault(BuildContext context, WidgetRef ref, ReviewPrompt p) async {
    try {
      await ref.read(apiClientProvider).upsertAgent(p.copyWith(isDefault: true).toJson());
      ref.invalidate(agentsProvider);
      if (context.mounted) showToast(context, '"${p.name}" is now active');
    } catch (e) {
      if (context.mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _delete(BuildContext context, WidgetRef ref, ReviewPrompt p) async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (_) => AlertDialog(
        title: const Text('Remove prompt?'),
        content: Text('Remove "${p.name}"?'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(context, false), child: const Text('Cancel')),
          FilledButton(onPressed: () => Navigator.pop(context, true), child: const Text('Remove')),
        ],
      ),
    );
    if (ok != true) return;
    try {
      await ref.read(apiClientProvider).deleteAgent(p.id);
      ref.invalidate(agentsProvider);
    } catch (e) {
      if (context.mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _openEditor(BuildContext context, WidgetRef ref, ReviewPrompt? existing) async {
    final saved = await showDialog<ReviewPrompt>(
      context: context,
      barrierDismissible: false,
      builder: (_) => _PromptEditorDialog(prompt: existing),
    );
    if (saved == null) return;
    try {
      await ref.read(apiClientProvider).upsertAgent(saved.toJson());
      ref.invalidate(agentsProvider);
      if (context.mounted) showToast(context, 'Prompt saved');
    } catch (e) {
      if (context.mounted) showToast(context, 'Error: $e', isError: true);
    }
  }
}

// ── Preset card ───────────────────────────────────────────────────────────────

class _PresetCard extends StatelessWidget {
  final PresetDef preset;
  final bool added;
  final VoidCallback? onAdd;
  final VoidCallback? onActivate;
  const _PresetCard({required this.preset, required this.added, this.onAdd, this.onActivate});

  @override
  Widget build(BuildContext context) {
    return Container(
      width: 160,
      margin: const EdgeInsets.only(right: 10),
      child: Card(
        child: InkWell(
          borderRadius: BorderRadius.circular(12),
          onTap: added ? onActivate : onAdd,
          child: Padding(
            padding: const EdgeInsets.all(12),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(children: [
                  Text(_focusEmoji(preset.focus), style: const TextStyle(fontSize: 18)),
                  const Spacer(),
                  if (added)
                    Icon(Icons.check_circle, size: 16,
                        color: Theme.of(context).colorScheme.primary)
                  else
                    Icon(Icons.add_circle_outline, size: 16, color: Colors.grey.shade500),
                ]),
                const SizedBox(height: 6),
                Text(preset.name,
                    style: const TextStyle(fontWeight: FontWeight.w600, fontSize: 13),
                    maxLines: 2, overflow: TextOverflow.ellipsis),
                const Spacer(),
                Text(added ? 'Tap to activate' : 'Tap to add',
                    style: TextStyle(fontSize: 10, color: Colors.grey.shade500)),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

// ── Prompt tile ───────────────────────────────────────────────────────────────

class _PromptTile extends StatelessWidget {
  final ReviewPrompt prompt;
  final VoidCallback onEdit, onDelete, onActivate;
  const _PromptTile({required this.prompt, required this.onEdit,
      required this.onDelete, required this.onActivate});

  @override
  Widget build(BuildContext context) {
    return Card(
      margin: const EdgeInsets.only(bottom: 6),
      child: ListTile(
        leading: Text(_focusEmoji(prompt.focus), style: const TextStyle(fontSize: 22)),
        title: Row(children: [
          Text(prompt.name, style: const TextStyle(fontWeight: FontWeight.w600)),
          if (prompt.isDefault) ...[
            const SizedBox(width: 8),
            Container(
              padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
              decoration: BoxDecoration(
                color: Theme.of(context).colorScheme.primary,
                borderRadius: BorderRadius.circular(8),
              ),
              child: const Text('ACTIVE',
                  style: TextStyle(color: Colors.white, fontSize: 10,
                      fontWeight: FontWeight.bold)),
            ),
          ],
        ]),
        subtitle: Text(
          prompt.instructions.isNotEmpty ? prompt.instructions : 'Custom template',
          maxLines: 1, overflow: TextOverflow.ellipsis,
          style: const TextStyle(fontSize: 12),
        ),
        trailing: Row(mainAxisSize: MainAxisSize.min, children: [
          if (!prompt.isDefault)
            TextButton(onPressed: onActivate, child: const Text('Activate')),
          IconButton(icon: const Icon(Icons.edit, size: 18), onPressed: onEdit),
          IconButton(
            icon: const Icon(Icons.delete, size: 18),
            color: Colors.red.shade400,
            onPressed: onDelete,
          ),
        ]),
        onTap: onEdit,
      ),
    );
  }
}

// ── Active banner ─────────────────────────────────────────────────────────────

class _ActiveBanner extends StatelessWidget {
  final ReviewPrompt prompt;
  const _ActiveBanner({required this.prompt});

  @override
  Widget build(BuildContext context) {
    return Container(
      width: double.infinity,
      margin: const EdgeInsets.fromLTRB(16, 12, 16, 0),
      padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 10),
      decoration: BoxDecoration(
        color: Theme.of(context).colorScheme.primaryContainer.withValues(alpha: 0.4),
        border: Border.all(color: Theme.of(context).colorScheme.primary.withValues(alpha: 0.4)),
        borderRadius: BorderRadius.circular(10),
      ),
      child: Row(children: [
        Text(_focusEmoji(prompt.focus), style: const TextStyle(fontSize: 20)),
        const SizedBox(width: 10),
        Expanded(child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text('Active: ${prompt.name}',
                style: const TextStyle(fontWeight: FontWeight.w600, fontSize: 13)),
            if (prompt.instructions.isNotEmpty)
              Text(prompt.instructions, maxLines: 1, overflow: TextOverflow.ellipsis,
                  style: const TextStyle(fontSize: 11, color: Colors.grey)),
          ],
        )),
      ]),
    );
  }
}

class _InfoBanner extends StatelessWidget {
  final String message;
  const _InfoBanner(this.message);

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 12, 16, 0),
      child: Text(message, style: const TextStyle(color: Colors.grey, fontSize: 13)),
    );
  }
}

class _SectionHeader extends StatelessWidget {
  final String title;
  final Widget? trailing;
  const _SectionHeader({required this.title, this.trailing});

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 14, 16, 6),
      child: Row(children: [
        Text(title, style: Theme.of(context).textTheme.titleSmall
            ?.copyWith(fontWeight: FontWeight.bold)),
        const Spacer(),
        if (trailing != null) trailing!,
      ]),
    );
  }
}

// ── Editor dialog ─────────────────────────────────────────────────────────────

class _PromptEditorDialog extends StatefulWidget {
  final ReviewPrompt? prompt;
  const _PromptEditorDialog({this.prompt});

  @override
  State<_PromptEditorDialog> createState() => _PromptEditorDialogState();
}

class _PromptEditorDialogState extends State<_PromptEditorDialog>
    with SingleTickerProviderStateMixin {
  final _idCtrl = TextEditingController();
  final _nameCtrl = TextEditingController();
  final _instrCtrl = TextEditingController();
  final _templateCtrl = TextEditingController();
  final _flagsCtrl = TextEditingController();
  String _focus = 'general';
  bool _isDefault = false;
  late final TabController _tabCtrl;

  @override
  void initState() {
    super.initState();
    _tabCtrl = TabController(length: 2, vsync: this);
    final p = widget.prompt;
    if (p != null) {
      _idCtrl.text = p.id;
      _nameCtrl.text = p.name;
      _instrCtrl.text = p.instructions;
      _templateCtrl.text = p.prompt;
      _flagsCtrl.text = p.cliFlags;
      _focus = p.focus;
      _isDefault = p.isDefault;
    }
  }

  @override
  void dispose() {
    _tabCtrl.dispose();
    _idCtrl.dispose();
    _nameCtrl.dispose();
    _instrCtrl.dispose();
    _templateCtrl.dispose();
    _flagsCtrl.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final isNew = widget.prompt == null;
    return Dialog(
      child: SizedBox(
        width: 720,
        height: 660,
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              // Header
              Row(children: [
                Text(isNew ? 'New Prompt' : 'Edit Prompt',
                    style: Theme.of(context).textTheme.titleLarge),
                const Spacer(),
                IconButton(icon: const Icon(Icons.close),
                    onPressed: () => Navigator.pop(context)),
              ]),
              const SizedBox(height: 16),

              // Name + focus row
              Row(children: [
                Expanded(child: TextFormField(
                  controller: _nameCtrl,
                  decoration: const InputDecoration(
                      labelText: 'Name', border: OutlineInputBorder()),
                )),
                const SizedBox(width: 12),
                DropdownButtonFormField<String>(
                  // ignore: deprecated_member_use
                  value: _focus,
                  decoration: const InputDecoration(
                      labelText: 'Focus', border: OutlineInputBorder()),
                  items: const [
                    DropdownMenuItem(value: 'general',      child: Text('🔍  General')),
                    DropdownMenuItem(value: 'security',     child: Text('🔒  Security')),
                    DropdownMenuItem(value: 'performance',  child: Text('⚡  Performance')),
                    DropdownMenuItem(value: 'architecture', child: Text('🏛️  Architecture')),
                    DropdownMenuItem(value: 'docs',         child: Text('📝  Docs & Style')),
                    DropdownMenuItem(value: 'custom',       child: Text('✨  Custom')),
                  ],
                  onChanged: (v) => setState(() => _focus = v!),
                ),
              ]),
              const SizedBox(height: 12),

              // Tabs: Instructions | Advanced
              TabBar(
                controller: _tabCtrl,
                tabs: const [
                  Tab(text: 'Instructions'),
                  Tab(text: 'Advanced (full template)'),
                ],
                labelStyle: const TextStyle(fontSize: 13),
              ),
              const SizedBox(height: 8),

              Expanded(
                child: TabBarView(
                  controller: _tabCtrl,
                  children: [
                    // Tab 1: Instructions (simple mode)
                    Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        const Text(
                          'Describe what to look for. Heimdallr will inject these '
                          'instructions into its default review template.',
                          style: TextStyle(fontSize: 12, color: Colors.grey),
                        ),
                        const SizedBox(height: 8),
                        Expanded(
                          child: TextFormField(
                            controller: _instrCtrl,
                            maxLines: null, expands: true,
                            decoration: const InputDecoration(
                              hintText: 'e.g. Focus on security vulnerabilities and '
                                  'potential injection attacks...',
                              border: OutlineInputBorder(),
                              alignLabelWithHint: true,
                            ),
                          ),
                        ),
                        const SizedBox(height: 8),
                        TextFormField(
                          controller: _flagsCtrl,
                          decoration: const InputDecoration(
                            labelText: 'Extra CLI flags (optional)',
                            hintText: '--model claude-opus-4-6',
                            border: OutlineInputBorder(),
                            isDense: true,
                            helperText:
                                'Passed directly to the AI binary (claude, gemini, codex)',
                          ),
                        ),
                      ],
                    ),

                    // Tab 2: Full template (advanced)
                    Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        const Text(
                          'Override the entire prompt. When set, Instructions are ignored.',
                          style: TextStyle(fontSize: 12, color: Colors.grey),
                        ),
                        const SizedBox(height: 4),
                        Wrap(
                          spacing: 6, runSpacing: 4,
                          children: ReviewPrompt.placeholders.map((p) => ActionChip(
                            label: Text(p, style: const TextStyle(
                                fontSize: 11, fontFamily: 'monospace')),
                            padding: EdgeInsets.zero,
                            onPressed: () {
                              final sel = _templateCtrl.selection;
                              final text = _templateCtrl.text;
                              final pos = sel.isValid ? sel.baseOffset : text.length;
                              _templateCtrl.text =
                                  text.substring(0, pos) + p + text.substring(pos);
                              _templateCtrl.selection =
                                  TextSelection.collapsed(offset: pos + p.length);
                            },
                          )).toList(),
                        ),
                        const SizedBox(height: 6),
                        Expanded(
                          child: TextFormField(
                            controller: _templateCtrl,
                            maxLines: null, expands: true,
                            style: const TextStyle(fontSize: 12, fontFamily: 'monospace'),
                            decoration: const InputDecoration(
                              border: OutlineInputBorder(),
                              alignLabelWithHint: true,
                            ),
                          ),
                        ),
                      ],
                    ),
                  ],
                ),
              ),

              const SizedBox(height: 12),
              Row(children: [
                Switch(value: _isDefault,
                    onChanged: (v) => setState(() => _isDefault = v)),
                const Text('Set as active'),
                const Spacer(),
                TextButton(onPressed: () => Navigator.pop(context),
                    child: const Text('Cancel')),
                const SizedBox(width: 8),
                FilledButton(
                  onPressed: () {
                    final id = isNew
                        ? _idCtrl.text.trim().isNotEmpty
                            ? _idCtrl.text.trim()
                            : 'prompt-${DateTime.now().millisecondsSinceEpoch}'
                        : widget.prompt!.id;
                    if (_nameCtrl.text.isEmpty) return;
                    Navigator.pop(context, ReviewPrompt(
                      id: id,
                      name: _nameCtrl.text.trim(),
                      focus: _focus,
                      instructions: _instrCtrl.text.trim(),
                      prompt: _templateCtrl.text.trim(),
                      cliFlags: _flagsCtrl.text.trim(),
                      isDefault: _isDefault,
                    ));
                  },
                  child: const Text('Save'),
                ),
              ]),
            ],
          ),
        ),
      ),
    );
  }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

String _focusEmoji(String focus) {
  switch (focus) {
    case 'security':     return '🔒';
    case 'performance':  return '⚡';
    case 'architecture': return '🏛️';
    case 'docs':         return '📝';
    case 'custom':       return '✨';
    default:             return '🔍';
  }
}
