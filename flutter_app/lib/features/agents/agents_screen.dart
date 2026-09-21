import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:mix/mix.dart';
import '../../core/models/agent.dart';
import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/keep_alive_tab.dart';
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
    // Active agent per category — each pipeline picks the agent whose
    // corresponding flag is set. Absent entries render as "built-in default"
    // in the banner so the user can see at a glance which categories have
    // explicit prompts and which fall through to the daemon default.
    final active = <PromptCategory, ReviewPrompt>{
      for (final c in PromptCategory.values)
        if (prompts.where((p) => p.isDefaultFor(c)).firstOrNull != null)
          c: prompts.firstWhere((p) => p.isDefaultFor(c)),
    };

    return DefaultTabController(
      length: 3,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          // Active prompts banner (above tabs) — shows one line per category
          _ActiveBanner(active: active),

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
                KeepAliveTab(
                  child: _CategoryTab(
                    category: PromptCategory.prReview,
                    prompts: prompts,
                    presets: ReviewPrompt.presets,
                    emptyMessage: 'Add a preset or create a custom prompt.',
                  ),
                ),
                KeepAliveTab(
                  child: _CategoryTab(
                    category: PromptCategory.issueTriage,
                    prompts: prompts,
                    presets: ReviewPrompt.issueTriagePresets,
                    emptyMessage:
                        'Tap a preset above or create a custom prompt to customise how issues are analysed.',
                  ),
                ),
                KeepAliveTab(
                  child: _CategoryTab(
                    category: PromptCategory.development,
                    prompts: prompts,
                    presets: ReviewPrompt.developmentPresets,
                    emptyMessage:
                        'Tap a preset above or create a custom prompt to customise auto-implementation.',
                  ),
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
  final PromptCategory category;
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
    final filtered = prompts
        .where(
          (p) => switch (category) {
            PromptCategory.prReview => p.hasPRReview,
            PromptCategory.issueTriage => p.hasIssueTriage,
            PromptCategory.development => p.hasDevelopment,
          },
        )
        .toList();

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
          height: 148,
          child: ListView(
            scrollDirection: Axis.horizontal,
            padding: const EdgeInsets.symmetric(horizontal: 16),
            children: presets.map((preset) {
              final stored = prompts
                  .where((p) => p.id == preset.id)
                  .firstOrNull;
              final alreadyActive = stored?.isDefaultFor(category) ?? false;
              return _PresetCard(
                preset: preset,
                added: stored != null,
                activeForCategory: alreadyActive,
                onAdd: stored == null
                    ? () => _addPreset(context, ref, preset)
                    : null,
                onActivate: stored != null && !alreadyActive
                    ? () => _setDefault(context, ref, stored, category)
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
                  category: category,
                  onEdit: () =>
                      _openEditor(context, ref, filtered[i], category),
                  onDelete: () => _delete(context, ref, filtered[i]),
                  onActivate: () =>
                      _setDefault(context, ref, filtered[i], category),
                );
              },
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
                    emptyMessage,
                    textAlign: TextAlign.center,
                  ),
                ),
              ),
            ),
          ),
      ],
    );
  }

  String _tileSubtitle(ReviewPrompt p) {
    switch (category) {
      case PromptCategory.prReview:
        return p.instructions.isNotEmpty ? p.instructions : 'Custom template';
      case PromptCategory.issueTriage:
        return p.issueInstructions.isNotEmpty
            ? p.issueInstructions
            : 'Custom issue triage template';
      case PromptCategory.development:
        return p.implementInstructions.isNotEmpty
            ? p.implementInstructions
            : 'Custom development template';
    }
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

/// Activates `p` for `category` only — leaves the other two category flags
/// untouched, so activating a triage prompt no longer clobbers an active
/// PR review or development prompt.
Future<void> _setDefault(
  BuildContext context,
  WidgetRef ref,
  ReviewPrompt p,
  PromptCategory category,
) async {
  try {
    await ref
        .read(apiClientProvider)
        .upsertAgent(p.withActive(category, true).toJson());
    ref.invalidate(agentsProvider);
    if (context.mounted) {
      showToast(
        context,
        '"${p.name}" is now active for ${_categoryName(category)}',
      );
    }
  } catch (e) {
    if (context.mounted) showToast(context, 'Error: $e', isError: true);
  }
}

String _categoryName(PromptCategory c) {
  switch (c) {
    case PromptCategory.prReview:
      return 'PR Review';
    case PromptCategory.issueTriage:
      return 'Issue Triage';
    case PromptCategory.development:
      return 'Development';
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
  PromptCategory category,
) async {
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

  /// True when an agent with this preset id exists in the store (regardless
  /// of activation status).
  final bool added;

  /// True when the stored agent is currently active for the *enclosing tab's*
  /// category — controls the footer text and whether onActivate is a no-op.
  final bool activeForCategory;
  final VoidCallback? onAdd;
  final VoidCallback? onActivate;
  const _PresetCard({
    required this.preset,
    required this.added,
    required this.activeForCategory,
    this.onAdd,
    this.onActivate,
  });

  @override
  Widget build(BuildContext context) {
    final focusColor = _focusColor(context, preset.focus);
    final activeColor = _categoryColor(context, _categoryForPreset(preset));
    final borderColor = activeForCategory
        ? activeColor.withValues(alpha: 0.65)
        : added
        ? AppColors.border.resolve(context)
        : AppColors.border.resolve(context).withValues(alpha: 0.75);
    final backgroundColor = activeForCategory
        ? activeColor.withValues(alpha: 0.12)
        : added
        ? AppColors.surfaceRaised.resolve(context)
        : AppColors.surface.resolve(context);
    final String footer;
    if (!added) {
      footer = 'Tap to add';
    } else if (activeForCategory) {
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
            onTap: !added ? onAdd : (activeForCategory ? null : onActivate),
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
                        activeForCategory
                            ? Icons.check_circle
                            : added
                            ? Icons.check_circle_outline
                            : Icons.add_circle_outline,
                        size: 16,
                        color: activeForCategory
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
                    color: activeForCategory
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

  /// The tab this tile is rendered under. Drives the ACTIVE badge and
  /// whether the Activate button is shown — each are category-scoped, so
  /// an agent active for PR review does NOT show ACTIVE on the Issue
  /// Triage tab and vice versa.
  final PromptCategory category;
  final VoidCallback onEdit, onDelete, onActivate;
  final String? subtitleOverride;
  const _PromptTile({
    required this.prompt,
    required this.category,
    required this.onEdit,
    required this.onDelete,
    required this.onActivate,
    this.subtitleOverride,
  });

  @override
  Widget build(BuildContext context) {
    final isActive = prompt.isDefaultFor(category);
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
                      ? _categoryColor(context, category)
                      : focusColor.withValues(alpha: 0.14),
                  border: isActive
                      ? _categoryColor(context, category)
                      : focusColor.withValues(alpha: 0.35),
                ),
              ],
            ),
            subtitle: AppText.muted(
              subtitleOverride ??
                  (prompt.instructions.isNotEmpty
                      ? prompt.instructions
                      : 'Custom template'),
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

/// Shows the three per-category active prompts side by side so the user
/// can see at a glance which agent is driving each pipeline. Categories
/// with no active agent render as "built-in default" (grey) — that's the
/// zero-config state and the daemon's built-in templates take over.
class _ActiveBanner extends StatelessWidget {
  final Map<PromptCategory, ReviewPrompt> active;
  const _ActiveBanner({required this.active});

  @override
  Widget build(BuildContext context) {
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
        child: LayoutBuilder(
          builder: (context, constraints) {
            final compact = constraints.maxWidth < 720;
            final children = [
              _bannerRow(context, PromptCategory.prReview),
              _bannerRow(context, PromptCategory.issueTriage),
              _bannerRow(context, PromptCategory.development),
            ];
            if (compact) {
              return Column(
                children: [
                  for (var i = 0; i < children.length; i++) ...[
                    if (i > 0) _divider(context, Axis.horizontal),
                    Padding(
                      padding: const EdgeInsets.symmetric(vertical: 8),
                      child: children[i],
                    ),
                  ],
                ],
              );
            }
            return Row(
              children: [
                Expanded(child: children[0]),
                _divider(context, Axis.vertical),
                Expanded(child: children[1]),
                _divider(context, Axis.vertical),
                Expanded(child: children[2]),
              ],
            );
          },
        ),
      ),
    );
  }

  Widget _bannerRow(BuildContext context, PromptCategory c) {
    final p = active[c];
    final name = p?.name ?? 'Built-in default';
    final emoji = p != null ? _focusEmoji(p.focus) : '⚙️';
    final categoryColor = _categoryColor(context, c);
    return Row(
      children: [
        Text(emoji, style: const TextStyle(fontSize: 16)),
        const SizedBox(width: 8),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            mainAxisSize: MainAxisSize.min,
            children: [
              AppBadge(
                label: _categoryName(c),
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
    );
  }

  Widget _divider(BuildContext context, Axis axis) => Container(
    width: axis == Axis.vertical ? 1 : double.infinity,
    height: axis == Axis.vertical ? 36 : 1,
    margin: axis == Axis.vertical
        ? const EdgeInsets.symmetric(horizontal: 10)
        : const EdgeInsets.symmetric(vertical: 2),
    color: AppColors.border.resolve(context).withValues(alpha: 0.55),
  );
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
  final PromptCategory category;
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
  // One active flag per category — the editor lets the user activate the
  // prompt across any subset of categories in one save. Defaults mirror the
  // existing agent when editing; for a brand-new prompt we pre-tick the
  // active flag of the tab the user opened the editor from (they almost
  // always want the thing they just created to be active for that pipeline).
  bool _isDefaultPr = false;
  bool _isDefaultIssue = false;
  bool _isDefaultDev = false;
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
      _isDefaultPr = p.isDefaultPr;
      _isDefaultIssue = p.isDefaultIssue;
      _isDefaultDev = p.isDefaultDev;
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
  })
  _categoryFields() {
    switch (widget.category) {
      case PromptCategory.issueTriage:
        return (
          instrCtrl: _issueInstrCtrl,
          templateCtrl: _issueTemplateCtrl,
          instrHint:
              'e.g. Categorise by severity, suggest labels, identify duplicates...',
          instrDescription:
              'Describe how issues should be triaged. Heimdallm will inject '
              'these instructions into the issue triage pipeline.',
          templateDescription:
              'Override the entire issue triage prompt. When set, Instructions are ignored.',
          placeholders: ReviewPrompt.issuePlaceholders,
        );
      case PromptCategory.development:
        return (
          instrCtrl: _implInstrCtrl,
          templateCtrl: _implTemplateCtrl,
          instrHint:
              'e.g. Follow TDD, write tests first, keep functions under 30 lines...',
          instrDescription:
              'Describe how code should be implemented. Heimdallm will inject '
              'these instructions into the development pipeline.',
          templateDescription:
              'Override the entire development prompt. When set, Instructions are ignored.',
          placeholders: ReviewPrompt.implementPlaceholders,
        );
      case PromptCategory.prReview:
        return (
          instrCtrl: _instrCtrl,
          templateCtrl: _templateCtrl,
          instrHint:
              'e.g. Focus on security vulnerabilities and potential injection attacks...',
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
      case PromptCategory.prReview:
        return 'PR Review';
      case PromptCategory.issueTriage:
        return 'Issue Triage';
      case PromptCategory.development:
        return 'Development';
    }
  }

  @override
  Widget build(BuildContext context) {
    final isNew = widget.prompt == null;
    final fields = _categoryFields();

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
                    isNew
                        ? 'New $_categoryLabel Prompt'
                        : 'Edit $_categoryLabel Prompt',
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
                        AppText.muted(fields.instrDescription),
                        const SizedBox(height: 8),
                        Expanded(
                          child: TextFormField(
                            controller: fields.instrCtrl,
                            maxLines: null,
                            expands: true,
                            decoration: InputDecoration(
                              hintText: fields.instrHint,
                              border: const OutlineInputBorder(),
                              alignLabelWithHint: true,
                            ),
                          ),
                        ),
                        if (widget.category == PromptCategory.prReview) ...[
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
                      ],
                    ),

                    // Tab 2: Full template (advanced)
                    Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        AppText.muted(fields.templateDescription),
                        const SizedBox(height: 4),
                        Wrap(
                          spacing: 6,
                          runSpacing: 4,
                          children: fields.placeholders
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
                                    final sel = fields.templateCtrl.selection;
                                    final text = fields.templateCtrl.text;
                                    final pos = sel.isValid
                                        ? sel.baseOffset
                                        : text.length;
                                    fields.templateCtrl.text =
                                        text.substring(0, pos) +
                                        p +
                                        text.substring(pos);
                                    fields.templateCtrl.selection =
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
                            controller: fields.templateCtrl,
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
              // Active toggles — one per category so a single save can
              // activate the prompt across any combination of pipelines.
              AppSurface(
                elevation: AppSurfaceElevation.canvas,
                padding: const EdgeInsets.all(12),
                child: _ActiveToggles(
                  prReview: _isDefaultPr,
                  issueTriage: _isDefaultIssue,
                  development: _isDefaultDev,
                  onChanged: (c, v) => setState(() {
                    switch (c) {
                      case PromptCategory.prReview:
                        _isDefaultPr = v;
                      case PromptCategory.issueTriage:
                        _isDefaultIssue = v;
                      case PromptCategory.development:
                        _isDefaultDev = v;
                    }
                  }),
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
                      // Validate non-empty content for the active category
                      final hasContent = switch (widget.category) {
                        PromptCategory.prReview =>
                          _instrCtrl.text.trim().isNotEmpty ||
                              _templateCtrl.text.trim().isNotEmpty,
                        PromptCategory.issueTriage =>
                          _issueInstrCtrl.text.trim().isNotEmpty ||
                              _issueTemplateCtrl.text.trim().isNotEmpty,
                        PromptCategory.development =>
                          _implInstrCtrl.text.trim().isNotEmpty ||
                              _implTemplateCtrl.text.trim().isNotEmpty,
                      };
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
                          isDefaultIssue: _isDefaultIssue,
                          isDefaultDev: _isDefaultDev,
                          issuePrompt: _issueTemplateCtrl.text.trim(),
                          issueInstructions: _issueInstrCtrl.text.trim(),
                          implementPrompt: _implTemplateCtrl.text.trim(),
                          implementInstructions: _implInstrCtrl.text.trim(),
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

/// Trio of Switch rows for the editor's "Set as active for ..." section.
/// Extracted so the editor's save button stays focused on persistence and
/// the widget tree around the three toggles stays shallow.
class _ActiveToggles extends StatelessWidget {
  final bool prReview, issueTriage, development;
  final void Function(PromptCategory category, bool value) onChanged;
  const _ActiveToggles({
    required this.prReview,
    required this.issueTriage,
    required this.development,
    required this.onChanged,
  });

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        const AppText.label('Set as active for'),
        const SizedBox(height: 4),
        _toggle(PromptCategory.prReview, 'PR Review', prReview),
        _toggle(PromptCategory.issueTriage, 'Issue Triage', issueTriage),
        _toggle(PromptCategory.development, 'Development', development),
      ],
    );
  }

  Widget _toggle(PromptCategory c, String label, bool value) => Row(
    children: [
      SizedBox(
        height: 28,
        child: Switch(
          value: value,
          onChanged: (v) => onChanged(c, v),
          materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
        ),
      ),
      const SizedBox(width: 10),
      AppText(label),
    ],
  );
}

// ── Helpers ───────────────────────────────────────────────────────────────────

PromptCategory _categoryForPreset(PresetDef preset) {
  if (preset.issueInstructions.isNotEmpty) {
    return PromptCategory.issueTriage;
  }
  if (preset.implementInstructions.isNotEmpty) {
    return PromptCategory.development;
  }
  return PromptCategory.prReview;
}

Color _categoryColor(BuildContext context, PromptCategory category) {
  switch (category) {
    case PromptCategory.prReview:
      return AppColors.featurePrReview.resolve(context);
    case PromptCategory.issueTriage:
      return AppColors.featureIssueTracking.resolve(context);
    case PromptCategory.development:
      return AppColors.featureDevelop.resolve(context);
  }
}

Color _focusColor(BuildContext context, String focus) {
  switch (focus) {
    case 'security':
      return AppColors.danger.resolve(context);
    case 'performance':
      return AppColors.info.resolve(context);
    case 'architecture':
      return AppColors.featureDevelop.resolve(context);
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
