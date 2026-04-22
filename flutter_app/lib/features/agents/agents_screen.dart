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

// ── Category enum ────────────────────────────────────────────────────────────

enum _PromptCategory { prReview, issueTriage, development }

// ── Main view ─────────────────────────────────────────────────────────────────

class _PromptsView extends ConsumerWidget {
  final List<ReviewPrompt> prompts;
  const _PromptsView({required this.prompts});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final activePrompt = prompts.where((p) => p.isDefault).firstOrNull;

    return DefaultTabController(
      length: 3,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          // Active prompt banner (above tabs)
          if (activePrompt != null)
            _ActiveBanner(prompt: activePrompt)
          else
            const _InfoBanner('No active prompt. Select one to customise review behaviour.'),

          const SizedBox(height: 8),

          // Category tabs
          const TabBar(
            tabs: [
              Tab(icon: Icon(Icons.rate_review, size: 18), text: 'PR Review'),
              Tab(icon: Icon(Icons.bug_report, size: 18), text: 'Issue Triage'),
              Tab(icon: Icon(Icons.code, size: 18), text: 'Development'),
            ],
            labelStyle: TextStyle(fontSize: 12, fontWeight: FontWeight.w600),
            unselectedLabelStyle: TextStyle(fontSize: 12),
            indicatorSize: TabBarIndicatorSize.label,
          ),

          // Tab content
          Expanded(
            child: TabBarView(
              children: [
                _CategoryTab(
                  category: _PromptCategory.prReview,
                  prompts: prompts,
                  presets: ReviewPrompt.presets,
                  emptyMessage: 'Add a preset or create a custom prompt.',
                ),
                _CategoryTab(
                  category: _PromptCategory.issueTriage,
                  prompts: prompts,
                  presets: ReviewPrompt.issueTriagePresets,
                  emptyMessage:
                      'Tap a preset above or create a custom prompt to customise how issues are analysed.',
                ),
                _CategoryTab(
                  category: _PromptCategory.development,
                  prompts: prompts,
                  presets: ReviewPrompt.developmentPresets,
                  emptyMessage:
                      'Tap a preset above or create a custom prompt to customise auto-implementation.',
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

// ── Category tab (shared between PR Review, Issue Triage, Development) ───────

/// Renders a horizontal preset row at the top + the list of already-added
/// prompts below it. The per-category differences (which agents filter into
/// the list, which subtitle to render on each tile, whether the Activate
/// button appears on the tile trailing row) are parameterised off the
/// category enum — otherwise the three tabs were ~130 lines of near-duplicate
/// scaffolding.
class _CategoryTab extends ConsumerWidget {
  final _PromptCategory category;
  final List<ReviewPrompt> prompts;
  final List<PresetDef> presets;
  final String emptyMessage;
  const _CategoryTab({
    required this.category,
    required this.prompts,
    required this.presets,
    required this.emptyMessage,
  });

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final filtered = prompts.where((p) => switch (category) {
          _PromptCategory.prReview => p.hasPRReview,
          _PromptCategory.issueTriage => p.hasIssueTriage,
          _PromptCategory.development => p.hasDevelopment,
        }).toList();

    // Only PR Review surfaces the Activate button on the tile trailing row —
    // for Issue Triage / Development activation flows through the editor's
    // "Set as active" toggle (the agent record carries all three categories
    // and activating it wires all of them simultaneously).
    final showActivateOnTile = category == _PromptCategory.prReview;

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _SectionHeader(
          title: 'Presets',
          trailing: TextButton.icon(
            icon: const Icon(Icons.add, size: 16),
            label: const Text('Custom'),
            onPressed: () => _openEditor(context, ref, null, category),
          ),
        ),
        SizedBox(
          height: 130,
          child: ListView(
            scrollDirection: Axis.horizontal,
            padding: const EdgeInsets.symmetric(horizontal: 16),
            children: presets.map((preset) {
              final already = prompts.any((p) => p.id == preset.id);
              return _PresetCard(
                preset: preset,
                added: already,
                onAdd: already ? null : () => _addPreset(context, ref, preset),
                onActivate: already
                    ? () => _setDefault(context, ref,
                        prompts.firstWhere((p) => p.id == preset.id))
                    : null,
              );
            }).toList(),
          ),
        ),
        const SizedBox(height: 8),
        if (filtered.isNotEmpty) ...[
          const _SectionHeader(title: 'My Prompts'),
          Expanded(
            child: ListView.builder(
              padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 4),
              itemCount: filtered.length,
              itemBuilder: (_, i) {
                final subtitle = _tileSubtitle(filtered[i]);
                return _PromptTile(
                  prompt: filtered[i],
                  subtitleOverride: subtitle,
                  showActivate: showActivateOnTile,
                  onEdit: () => _openEditor(context, ref, filtered[i], category),
                  onDelete: () => _delete(context, ref, filtered[i]),
                  onActivate: () => _setDefault(context, ref, filtered[i]),
                );
              },
            ),
          ),
        ] else
          Expanded(
            child: Center(
              child: Text(emptyMessage, style: const TextStyle(color: Colors.grey)),
            ),
          ),
      ],
    );
  }

  String _tileSubtitle(ReviewPrompt p) {
    switch (category) {
      case _PromptCategory.prReview:
        return p.instructions.isNotEmpty ? p.instructions : 'Custom template';
      case _PromptCategory.issueTriage:
        return p.issueInstructions.isNotEmpty
            ? p.issueInstructions
            : 'Custom issue triage template';
      case _PromptCategory.development:
        return p.implementInstructions.isNotEmpty
            ? p.implementInstructions
            : 'Custom development template';
    }
  }
}

// ── Shared actions ───────────────────────────────────────────────────────────

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

Future<void> _openEditor(BuildContext context, WidgetRef ref, ReviewPrompt? existing, _PromptCategory category) async {
  final saved = await showDialog<ReviewPrompt>(
    context: context,
    barrierDismissible: false,
    builder: (_) => _PromptEditorDialog(prompt: existing, category: category),
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
  final String? subtitleOverride;
  final bool showActivate;
  const _PromptTile({required this.prompt, required this.onEdit,
      required this.onDelete, required this.onActivate, this.subtitleOverride,
      this.showActivate = true});

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
          subtitleOverride ?? (prompt.instructions.isNotEmpty ? prompt.instructions : 'Custom template'),
          maxLines: 1, overflow: TextOverflow.ellipsis,
          style: const TextStyle(fontSize: 12),
        ),
        trailing: Row(mainAxisSize: MainAxisSize.min, children: [
          if (showActivate && !prompt.isDefault)
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
  final _PromptCategory category;
  const _PromptEditorDialog({this.prompt, required this.category});

  @override
  State<_PromptEditorDialog> createState() => _PromptEditorDialogState();
}

class _PromptEditorDialogState extends State<_PromptEditorDialog>
    with SingleTickerProviderStateMixin {
  final _idCtrl = TextEditingController();
  final _nameCtrl = TextEditingController();
  // PR Review fields
  final _instrCtrl = TextEditingController();
  final _templateCtrl = TextEditingController();
  final _flagsCtrl = TextEditingController();
  // Issue Triage fields
  final _issueInstrCtrl = TextEditingController();
  final _issueTemplateCtrl = TextEditingController();
  // Development fields
  final _implInstrCtrl = TextEditingController();
  final _implTemplateCtrl = TextEditingController();

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
      _issueInstrCtrl.text = p.issueInstructions;
      _issueTemplateCtrl.text = p.issuePrompt;
      _implInstrCtrl.text = p.implementInstructions;
      _implTemplateCtrl.text = p.implementPrompt;
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
    _issueInstrCtrl.dispose();
    _issueTemplateCtrl.dispose();
    _implInstrCtrl.dispose();
    _implTemplateCtrl.dispose();
    super.dispose();
  }

  /// Returns the relevant instructions/template controllers and labels for the current category.
  ({
    TextEditingController instrCtrl,
    TextEditingController templateCtrl,
    String instrHint,
    String instrDescription,
    String templateDescription,
    List<String> placeholders,
  }) _categoryFields() {
    switch (widget.category) {
      case _PromptCategory.issueTriage:
        return (
          instrCtrl: _issueInstrCtrl,
          templateCtrl: _issueTemplateCtrl,
          instrHint: 'e.g. Categorise by severity, suggest labels, identify duplicates...',
          instrDescription:
              'Describe how issues should be triaged. Heimdallm will inject '
              'these instructions into the issue triage pipeline.',
          templateDescription:
              'Override the entire issue triage prompt. When set, Instructions are ignored.',
          placeholders: ReviewPrompt.issuePlaceholders,
        );
      case _PromptCategory.development:
        return (
          instrCtrl: _implInstrCtrl,
          templateCtrl: _implTemplateCtrl,
          instrHint: 'e.g. Follow TDD, write tests first, keep functions under 30 lines...',
          instrDescription:
              'Describe how code should be implemented. Heimdallm will inject '
              'these instructions into the development pipeline.',
          templateDescription:
              'Override the entire development prompt. When set, Instructions are ignored.',
          placeholders: ReviewPrompt.implementPlaceholders,
        );
      case _PromptCategory.prReview:
        return (
          instrCtrl: _instrCtrl,
          templateCtrl: _templateCtrl,
          instrHint: 'e.g. Focus on security vulnerabilities and potential injection attacks...',
          instrDescription:
              'Describe what to look for. Heimdallm will inject these '
              'instructions into its default review template.',
          templateDescription:
              'Override the entire prompt. When set, Instructions are ignored.',
          placeholders: ReviewPrompt.placeholders,
        );
    }
  }

  String get _categoryLabel {
    switch (widget.category) {
      case _PromptCategory.prReview: return 'PR Review';
      case _PromptCategory.issueTriage: return 'Issue Triage';
      case _PromptCategory.development: return 'Development';
    }
  }

  @override
  Widget build(BuildContext context) {
    final isNew = widget.prompt == null;
    final fields = _categoryFields();

    return Dialog(
      child: SizedBox(
        width: 720,
        height: 660,
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              // Header
              Row(children: [
                Text(isNew ? 'New $_categoryLabel Prompt' : 'Edit $_categoryLabel Prompt',
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
                SizedBox(
                  width: 200,
                  child: DropdownButtonFormField<String>(
                    // ignore: deprecated_member_use
                    value: _focus,
                    decoration: const InputDecoration(
                        labelText: 'Focus', border: OutlineInputBorder()),
                    items: const [
                      DropdownMenuItem(value: 'general',      child: Text('General')),
                      DropdownMenuItem(value: 'security',     child: Text('Security')),
                      DropdownMenuItem(value: 'performance',  child: Text('Performance')),
                      DropdownMenuItem(value: 'architecture', child: Text('Architecture')),
                      DropdownMenuItem(value: 'docs',         child: Text('Docs & Style')),
                      DropdownMenuItem(value: 'custom',       child: Text('Custom')),
                    ],
                    onChanged: (v) => setState(() => _focus = v!),
                  ),
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
                        Text(
                          fields.instrDescription,
                          style: const TextStyle(fontSize: 12, color: Colors.grey),
                        ),
                        const SizedBox(height: 8),
                        Expanded(
                          child: TextFormField(
                            controller: fields.instrCtrl,
                            maxLines: null, expands: true,
                            decoration: InputDecoration(
                              hintText: fields.instrHint,
                              border: const OutlineInputBorder(),
                              alignLabelWithHint: true,
                            ),
                          ),
                        ),
                        if (widget.category == _PromptCategory.prReview) ...[
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
                      ],
                    ),

                    // Tab 2: Full template (advanced)
                    Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          fields.templateDescription,
                          style: const TextStyle(fontSize: 12, color: Colors.grey),
                        ),
                        const SizedBox(height: 4),
                        Wrap(
                          spacing: 6, runSpacing: 4,
                          children: fields.placeholders.map((p) => ActionChip(
                            label: Text(p, style: const TextStyle(
                                fontSize: 11, fontFamily: 'monospace')),
                            padding: EdgeInsets.zero,
                            onPressed: () {
                              final sel = fields.templateCtrl.selection;
                              final text = fields.templateCtrl.text;
                              final pos = sel.isValid ? sel.baseOffset : text.length;
                              fields.templateCtrl.text =
                                  text.substring(0, pos) + p + text.substring(pos);
                              fields.templateCtrl.selection =
                                  TextSelection.collapsed(offset: pos + p.length);
                            },
                          )).toList(),
                        ),
                        const SizedBox(height: 6),
                        Expanded(
                          child: TextFormField(
                            controller: fields.templateCtrl,
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
                    // Validate non-empty content for the active category
                    final hasContent = switch (widget.category) {
                      _PromptCategory.prReview =>
                        _instrCtrl.text.trim().isNotEmpty || _templateCtrl.text.trim().isNotEmpty,
                      _PromptCategory.issueTriage =>
                        _issueInstrCtrl.text.trim().isNotEmpty || _issueTemplateCtrl.text.trim().isNotEmpty,
                      _PromptCategory.development =>
                        _implInstrCtrl.text.trim().isNotEmpty || _implTemplateCtrl.text.trim().isNotEmpty,
                    };
                    if (!hasContent) {
                      ScaffoldMessenger.of(context).showSnackBar(
                        const SnackBar(content: Text('Please provide instructions or a template')),
                      );
                      return;
                    }
                    Navigator.pop(context, ReviewPrompt(
                      id: id,
                      name: _nameCtrl.text.trim(),
                      focus: _focus,
                      instructions: _instrCtrl.text.trim(),
                      prompt: _templateCtrl.text.trim(),
                      cliFlags: _flagsCtrl.text.trim(),
                      isDefault: _isDefault,
                      issuePrompt: _issueTemplateCtrl.text.trim(),
                      issueInstructions: _issueInstrCtrl.text.trim(),
                      implementPrompt: _implTemplateCtrl.text.trim(),
                      implementInstructions: _implInstrCtrl.text.trim(),
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
