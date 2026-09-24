import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:mix/mix.dart';
import '../../core/models/agent.dart';
import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
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
      error: (e, _) =>
          Center(child: AppText('Error: $e', textAlign: TextAlign.center)),
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
    // The daemon reviews with whichever prompt carries the active flag; none
    // means its built-in default template.
    final active = prompts.where((p) => p.isDefaultPr).firstOrNull;

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _ActiveBanner(active: active),
        const SizedBox(height: 8),
        Expanded(child: _PromptsList(prompts: prompts)),
      ],
    );
  }
}

// ── Prompt list ───────────────────────────────────────────────────────────────

/// Renders a horizontal preset row at the top + the list of already-added
/// review prompts below it.
class _PromptsList extends ConsumerWidget {
  final List<ReviewPrompt> prompts;
  const _PromptsList({required this.prompts});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final filtered = prompts.where((p) => p.hasPRReview).toList();

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _SectionHeader(
          title: 'Presets',
          trailing: TextButton.icon(
            icon: const Icon(Icons.add, size: 16),
            label: const Text('Custom'),
            onPressed: () => _openEditor(context, ref, null),
          ),
        ),
        SizedBox(
          height: 148,
          child: ListView(
            scrollDirection: Axis.horizontal,
            padding: const EdgeInsets.symmetric(horizontal: 16),
            children: ReviewPrompt.presets.map((preset) {
              final stored = prompts
                  .where((p) => p.id == preset.id)
                  .firstOrNull;
              final alreadyActive = stored?.isDefaultPr ?? false;
              return _PresetCard(
                preset: preset,
                added: stored != null,
                active: alreadyActive,
                onAdd: stored == null
                    ? () => _addPreset(context, ref, preset)
                    : null,
                onActivate: stored != null && !alreadyActive
                    ? () => _setDefault(context, ref, stored)
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
              itemBuilder: (_, i) => _PromptTile(
                prompt: filtered[i],
                onEdit: () => _openEditor(context, ref, filtered[i]),
                onDelete: () => _delete(context, ref, filtered[i]),
                onActivate: () => _setDefault(context, ref, filtered[i]),
              ),
            ),
          ),
        ] else
          Expanded(
            child: Center(
              child: Padding(
                padding: const EdgeInsets.symmetric(horizontal: 24),
                child: AppSurface(
                  elevation: AppSurfaceElevation.canvas,
                  padding: const EdgeInsets.all(16),
                  child: AppText.muted(
                    'Add a preset or create a custom prompt.',
                    textAlign: TextAlign.center,
                  ),
                ),
              ),
            ),
          ),
      ],
    );
  }
}

// ── Shared actions ───────────────────────────────────────────────────────────

Future<void> _addPreset(
  BuildContext context,
  WidgetRef ref,
  PresetDef preset,
) async {
  final p = ReviewPrompt.fromPreset(preset);
  try {
    await ref.read(apiClientProvider).upsertAgent(p.toJson());
    ref.invalidate(agentsProvider);
  } catch (e) {
    if (context.mounted) showToast(context, 'Error: $e', isError: true);
  }
}

/// Makes `p` the active review prompt. The daemon clears the flag on every
/// other agent in the same transaction.
Future<void> _setDefault(
  BuildContext context,
  WidgetRef ref,
  ReviewPrompt p,
) async {
  try {
    await ref
        .read(apiClientProvider)
        .upsertAgent(p.copyWith(isDefaultPr: true).toJson());
    ref.invalidate(agentsProvider);
    if (context.mounted) {
      showToast(context, '"${p.name}" is now the active review prompt');
    }
  } catch (e) {
    if (context.mounted) showToast(context, 'Error: $e', isError: true);
  }
}

Future<void> _delete(
  BuildContext context,
  WidgetRef ref,
  ReviewPrompt p,
) async {
  final ok = await showDialog<bool>(
    context: context,
    builder: (_) => Dialog(
      backgroundColor: Colors.transparent,
      insetPadding: const EdgeInsets.symmetric(horizontal: 24, vertical: 24),
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 420),
        child: AppSurface(
          radius: AppRadius.lg,
          padding: const EdgeInsets.all(24),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              const AppText.sectionTitle('Remove prompt?'),
              const SizedBox(height: 8),
              AppText('Remove "${p.name}"?'),
              const SizedBox(height: 20),
              Row(
                mainAxisAlignment: MainAxisAlignment.end,
                children: [
                  AppButton.secondary(
                    label: 'Cancel',
                    onPressed: () => Navigator.pop(context, false),
                  ),
                  const SizedBox(width: 8),
                  AppButton.destructive(
                    label: 'Remove',
                    onPressed: () => Navigator.pop(context, true),
                  ),
                ],
              ),
            ],
          ),
        ),
      ),
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

Future<void> _openEditor(
  BuildContext context,
  WidgetRef ref,
  ReviewPrompt? existing,
) async {
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

// ── Preset card ───────────────────────────────────────────────────────────────

class _PresetCard extends StatelessWidget {
  final PresetDef preset;

  /// True when an agent with this preset id exists in the store (regardless
  /// of activation status).
  final bool added;

  /// True when the stored agent is the active review prompt — controls the
  /// footer text and whether onActivate is a no-op.
  final bool active;
  final VoidCallback? onAdd;
  final VoidCallback? onActivate;
  const _PresetCard({
    required this.preset,
    required this.added,
    required this.active,
    this.onAdd,
    this.onActivate,
  });

  @override
  Widget build(BuildContext context) {
    final focusColor = _focusColor(context, preset.focus);
    final activeColor = AppColors.featurePrReview.resolve(context);
    final borderColor = active
        ? activeColor.withValues(alpha: 0.65)
        : added
        ? AppColors.border.resolve(context)
        : AppColors.border.resolve(context).withValues(alpha: 0.75);
    final backgroundColor = active
        ? activeColor.withValues(alpha: 0.12)
        : added
        ? AppColors.surfaceRaised.resolve(context)
        : AppColors.surface.resolve(context);
    final String footer;
    if (!added) {
      footer = 'Tap to add';
    } else if (active) {
      footer = 'Active';
    } else {
      footer = 'Tap to activate';
    }
    return Container(
      width: 160,
      margin: const EdgeInsets.only(right: 10),
      child: Box(
        style: BoxStyler()
            .color(backgroundColor)
            .borderRadiusAll(AppRadius.lg())
            .borderAll(color: borderColor, width: 1),
        child: Material(
          color: Colors.transparent,
          child: InkWell(
            borderRadius: BorderRadius.circular(12),
            onTap: !added ? onAdd : (active ? null : onActivate),
            child: Padding(
              padding: const EdgeInsets.all(12),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Row(
                    children: [
                      Text(
                        _focusEmoji(preset.focus),
                        style: const TextStyle(fontSize: 18),
                      ),
                      const SizedBox(width: 8),
                      Expanded(
                        child: AppText.label(
                          _focusLabel(preset.focus),
                          color: focusColor,
                        ),
                      ),
                      Icon(
                        active
                            ? Icons.check_circle
                            : added
                            ? Icons.check_circle_outline
                            : Icons.add_circle_outline,
                        size: 16,
                        color: active
                            ? activeColor
                            : Theme.of(context).colorScheme.onSurfaceVariant,
                      ),
                    ],
                  ),
                  const SizedBox(height: 6),
                  AppText(
                    preset.name,
                    maxLines: 2,
                    overflow: TextOverflow.ellipsis,
                  ),
                  const Spacer(),
                  AppText.label(
                    footer,
                    color: active
                        ? activeColor
                        : Theme.of(context).colorScheme.onSurfaceVariant,
                  ),
                ],
              ),
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
  const _PromptTile({
    required this.prompt,
    required this.onEdit,
    required this.onDelete,
    required this.onActivate,
  });

  @override
  Widget build(BuildContext context) {
    final isActive = prompt.isDefaultPr;
    final activeColor = AppColors.featurePrReview.resolve(context);
    final focusColor = _focusColor(context, prompt.focus);
    return Padding(
      padding: const EdgeInsets.only(bottom: 6),
      child: AppSurface(
        child: Material(
          type: MaterialType.transparency,
          child: ListTile(
            leading: Text(
              _focusEmoji(prompt.focus),
              style: const TextStyle(fontSize: 22),
            ),
            title: Row(
              children: [
                Expanded(
                  child: StyledText(
                    prompt.name,
                    style: TextStyler()
                        .style(AppTextStyles.body.mix())
                        .fontWeight(FontWeight.w600)
                        .maxLines(1)
                        .overflow(TextOverflow.ellipsis),
                  ),
                ),
                const SizedBox(width: 8),
                AppBadge(
                  label: isActive ? 'ACTIVE' : _focusLabel(prompt.focus),
                  foreground: isActive
                      ? AppColors.onAccent.resolve(context)
                      : focusColor,
                  background: isActive
                      ? activeColor
                      : focusColor.withValues(alpha: 0.14),
                  border: isActive
                      ? activeColor
                      : focusColor.withValues(alpha: 0.35),
                ),
              ],
            ),
            subtitle: AppText.muted(
              prompt.instructions.isNotEmpty
                  ? prompt.instructions
                  : 'Custom template',
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
            ),
            trailing: Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                if (!isActive)
                  TextButton(
                    onPressed: onActivate,
                    child: const Text('Activate'),
                  ),
                IconButton(
                  icon: const Icon(Icons.edit, size: 18),
                  onPressed: onEdit,
                ),
                IconButton(
                  icon: const Icon(Icons.delete, size: 18),
                  color: AppColors.danger.resolve(context),
                  onPressed: onDelete,
                ),
              ],
            ),
            onTap: onEdit,
          ),
        ),
      ),
    );
  }
}

// ── Active banner ─────────────────────────────────────────────────────────────

/// Shows which prompt drives PR reviews. No active prompt renders as
/// "built-in default" (grey) — that's the zero-config state and the daemon's
/// built-in template takes over.
class _ActiveBanner extends StatelessWidget {
  final ReviewPrompt? active;
  const _ActiveBanner({required this.active});

  @override
  Widget build(BuildContext context) {
    final p = active;
    final name = p?.name ?? 'Built-in default';
    final emoji = p != null ? _focusEmoji(p.focus) : '⚙️';
    final categoryColor = AppColors.featurePrReview.resolve(context);
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 12, 16, 0),
      child: Box(
        style: BoxStyler()
            .color(
              AppColors.accentMuted.resolve(context).withValues(alpha: 0.18),
            )
            .borderAll(
              color: AppColors.accent.resolve(context).withValues(alpha: 0.25),
              width: 1,
            )
            .borderRadiusAll(AppRadius.lg())
            .padding(
              EdgeInsetsGeometryMix.value(
                const EdgeInsets.symmetric(horizontal: 14, vertical: 10),
              ),
            ),
        child: Row(
          children: [
            Text(emoji, style: const TextStyle(fontSize: 16)),
            const SizedBox(width: 8),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                mainAxisSize: MainAxisSize.min,
                children: [
                  AppBadge(
                    label: 'PR Review',
                    foreground: categoryColor,
                    background: categoryColor.withValues(alpha: 0.12),
                    border: categoryColor.withValues(alpha: 0.35),
                  ),
                  const SizedBox(height: 6),
                  StyledText(
                    name,
                    style: TextStyler()
                        .style(AppTextStyles.body.mix())
                        .fontWeight(FontWeight.w600)
                        .color(
                          p != null
                              ? AppColors.text.resolve(context)
                              : AppColors.textMuted.resolve(context),
                        )
                        .maxLines(1)
                        .overflow(TextOverflow.ellipsis),
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

class _SectionHeader extends StatelessWidget {
  final String title;
  final Widget? trailing;
  const _SectionHeader({required this.title, this.trailing});

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 14, 16, 6),
      child: Row(
        children: [
          AppText.sectionTitle(title),
          const Spacer(),
          trailing ?? const SizedBox.shrink(),
        ],
      ),
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
  bool _isDefaultPr = false;
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
      _isDefaultPr = p.isDefaultPr;
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
      backgroundColor: Colors.transparent,
      child: SizedBox(
        width: 720,
        height: 660,
        child: AppSurface(
          radius: AppRadius.lg,
          padding: const EdgeInsets.all(24),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              // Header
              Row(
                children: [
                  AppText.pageTitle(
                    isNew ? 'New Review Prompt' : 'Edit Review Prompt',
                  ),
                  const Spacer(),
                  IconButton(
                    icon: const Icon(Icons.close),
                    onPressed: () => Navigator.pop(context),
                  ),
                ],
              ),
              const SizedBox(height: 16),

              // Name + focus row
              Row(
                children: [
                  Expanded(
                    child: TextFormField(
                      controller: _nameCtrl,
                      decoration: const InputDecoration(
                        labelText: 'Name',
                        border: OutlineInputBorder(),
                      ),
                    ),
                  ),
                  const SizedBox(width: 12),
                  SizedBox(
                    width: 200,
                    child: DropdownButtonFormField<String>(
                      // ignore: deprecated_member_use
                      value: _focus,
                      decoration: const InputDecoration(
                        labelText: 'Focus',
                        border: OutlineInputBorder(),
                      ),
                      items: const [
                        DropdownMenuItem(
                          value: 'general',
                          child: Text('General'),
                        ),
                        DropdownMenuItem(
                          value: 'security',
                          child: Text('Security'),
                        ),
                        DropdownMenuItem(
                          value: 'performance',
                          child: Text('Performance'),
                        ),
                        DropdownMenuItem(
                          value: 'architecture',
                          child: Text('Architecture'),
                        ),
                        DropdownMenuItem(
                          value: 'docs',
                          child: Text('Docs & Style'),
                        ),
                        DropdownMenuItem(
                          value: 'custom',
                          child: Text('Custom'),
                        ),
                      ],
                      onChanged: (v) => setState(() => _focus = v!),
                    ),
                  ),
                ],
              ),
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
                        const AppText.muted(
                          'Describe what to look for. Heimdallm will inject these '
                          'instructions into its default review template.',
                        ),
                        const SizedBox(height: 8),
                        Expanded(
                          child: TextFormField(
                            controller: _instrCtrl,
                            maxLines: null,
                            expands: true,
                            decoration: const InputDecoration(
                              hintText:
                                  'e.g. Focus on security vulnerabilities and potential injection attacks...',
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
                            hintText: 'Flags allowed for the configured CLI',
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
                        const AppText.muted(
                          'Override the entire prompt. When set, Instructions are ignored.',
                        ),
                        const SizedBox(height: 4),
                        Wrap(
                          spacing: 6,
                          runSpacing: 4,
                          children: ReviewPrompt.placeholders
                              .map(
                                (p) => ActionChip(
                                  label: Text(
                                    p,
                                    style: const TextStyle(
                                      fontSize: 11,
                                      fontFamily: 'monospace',
                                    ),
                                  ),
                                  padding: EdgeInsets.zero,
                                  onPressed: () {
                                    final sel = _templateCtrl.selection;
                                    final text = _templateCtrl.text;
                                    final pos = sel.isValid
                                        ? sel.baseOffset
                                        : text.length;
                                    _templateCtrl.text =
                                        text.substring(0, pos) +
                                        p +
                                        text.substring(pos);
                                    _templateCtrl.selection =
                                        TextSelection.collapsed(
                                          offset: pos + p.length,
                                        );
                                  },
                                ),
                              )
                              .toList(),
                        ),
                        const SizedBox(height: 6),
                        Expanded(
                          child: TextFormField(
                            controller: _templateCtrl,
                            maxLines: null,
                            expands: true,
                            style: const TextStyle(
                              fontSize: 12,
                              fontFamily: 'monospace',
                            ),
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
              AppSurface(
                elevation: AppSurfaceElevation.canvas,
                padding: const EdgeInsets.all(12),
                child: Row(
                  children: [
                    SizedBox(
                      height: 28,
                      child: Switch(
                        value: _isDefaultPr,
                        onChanged: (v) => setState(() => _isDefaultPr = v),
                        materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
                      ),
                    ),
                    const SizedBox(width: 10),
                    const AppText('Use as the active review prompt'),
                  ],
                ),
              ),
              const SizedBox(height: 8),
              Row(
                children: [
                  const Spacer(),
                  AppButton.secondary(
                    label: 'Cancel',
                    onPressed: () => Navigator.pop(context),
                  ),
                  const SizedBox(width: 8),
                  AppButton(
                    label: 'Save',
                    onPressed: () {
                      final id = isNew
                          ? _idCtrl.text.trim().isNotEmpty
                                ? _idCtrl.text.trim()
                                : 'prompt-${DateTime.now().millisecondsSinceEpoch}'
                          : widget.prompt!.id;
                      if (_nameCtrl.text.isEmpty) return;
                      final hasContent =
                          _instrCtrl.text.trim().isNotEmpty ||
                          _templateCtrl.text.trim().isNotEmpty;
                      if (!hasContent) {
                        showToast(
                          context,
                          'Please provide instructions or a template',
                          isError: true,
                        );
                        return;
                      }
                      Navigator.pop(
                        context,
                        ReviewPrompt(
                          id: id,
                          name: _nameCtrl.text.trim(),
                          focus: _focus,
                          instructions: _instrCtrl.text.trim(),
                          prompt: _templateCtrl.text.trim(),
                          cliFlags: _flagsCtrl.text.trim(),
                          isDefaultPr: _isDefaultPr,
                        ),
                      );
                    },
                  ),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

Color _focusColor(BuildContext context, String focus) {
  switch (focus) {
    case 'security':
      return AppColors.danger.resolve(context);
    case 'performance':
      return AppColors.info.resolve(context);
    case 'architecture':
      return AppColors.success.resolve(context);
    case 'docs':
      return AppColors.warning.resolve(context);
    case 'custom':
      return AppColors.accent.resolve(context);
    default:
      return AppColors.textMuted.resolve(context);
  }
}

String _focusLabel(String focus) {
  switch (focus) {
    case 'security':
      return 'Security';
    case 'performance':
      return 'Performance';
    case 'architecture':
      return 'Architecture';
    case 'docs':
      return 'Documentation';
    case 'custom':
      return 'Custom';
    default:
      return 'General';
  }
}

String _focusEmoji(String focus) {
  switch (focus) {
    case 'security':
      return '🔒';
    case 'performance':
      return '⚡';
    case 'architecture':
      return '🏛️';
    case 'docs':
      return '📝';
    case 'custom':
      return '✨';
    default:
      return '🔍';
  }
}
