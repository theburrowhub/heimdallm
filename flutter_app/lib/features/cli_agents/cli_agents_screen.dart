import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/models/agent_schema.dart';
import '../../core/models/config_model.dart';
import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/toast.dart';
import '../agents/agents_screen.dart' show agentsProvider;
import '../config/config_providers.dart';
import '../../shared/widgets/review_limits_editor.dart';
import 'agent_schema_form.dart';
import 'agent_schemas_provider.dart';
import 'provider_model_field.dart';

const _defaultCliNames = ['claude', 'gemini', 'codex'];

/// Per-agent execution settings editor. With [agentIds] it edits only those
/// agents (the per-agent settings page opened from the catalog); the default
/// keeps the original three-agent page.
class CLIAgentsScreen extends ConsumerStatefulWidget {
  final List<String> agentIds;

  /// Models discovered from each CLI (catalog), preferred over the built-in
  /// lists when non-empty.
  final Map<String, List<String>> discoveredModels;

  /// Shown above the editor (the catalog's detection details).
  final Widget? header;

  /// Agents that are in-process API providers (OpenRouter). They have no CLI
  /// default model: the model is a required pick from [discoveredModels].
  final Set<String> providerIds;

  /// Whether [discoveredModels] is being (re)loaded.
  final bool modelsLoading;

  /// Providers whose API key is not set, so their models cannot load.
  final Set<String> providersWithoutKey;

  /// Reloads [discoveredModels] (the catalog rescan), for a provider whose
  /// model list could not be loaded.
  final Future<void> Function()? onReloadModels;

  const CLIAgentsScreen({
    super.key,
    this.agentIds = _defaultCliNames,
    this.discoveredModels = const {},
    this.header,
    this.providerIds = const {},
    this.modelsLoading = false,
    this.providersWithoutKey = const {},
    this.onReloadModels,
  });

  @override
  ConsumerState<CLIAgentsScreen> createState() => _CLIAgentsScreenState();
}

class _CLIAgentsScreenState extends ConsumerState<CLIAgentsScreen> {
  // Per-agent — keys match widget.agentIds
  late final Map<String, _AgentState> _agents = {
    for (final n in widget.agentIds) n: _AgentState(),
  };

  List<String> _modelsFor(String name, [AgentSchema? schema]) {
    final discovered = widget.discoveredModels[name] ?? const <String>[];
    if (discovered.isNotEmpty) return discovered;
    final fromSchema = schema?.field('model')?.options ?? const <String>[];
    return fromSchema.isNotEmpty
        ? fromSchema
        : CLIAgentConfig.modelOptions[name] ?? const <String>[];
  }

  bool _initialized = false;
  bool _saved = false;
  bool _saving = false;
  Timer? _debounce;
  Timer? _savedResetTimer;

  @override
  void dispose() {
    _debounce?.cancel();
    _savedResetTimer?.cancel();
    super.dispose();
  }

  void _initFrom(AppConfig config) {
    if (_initialized) return;
    _initialized = true;
    for (final name in widget.agentIds) {
      final ac = config.agentConfigs[name] ?? const CLIAgentConfig();
      _agents[name] = _AgentState.from(ac, modelOptions: _modelsFor(name));
    }
  }

  void _markDirty() {
    if (_saved) {
      setState(() => _saved = false);
    }
    _debounce?.cancel();
    _debounce = Timer(const Duration(milliseconds: 800), _autoSave);
  }

  Future<void> _autoSave() async {
    final current = ref.read(configNotifierProvider).value;
    if (current == null) return;
    await _save(current);
  }

  /// A field holds input it could not report (a number out of range):
  /// saving now would store the last valid value, not what the field shows.
  bool get _hasInvalidInput => _agents.values.any((a) => a.invalid.isNotEmpty);

  Future<void> _save(AppConfig current) async {
    if (_hasInvalidInput) return;
    if (mounted) {
      setState(() {
        _saving = true;
        _saved = false;
      });
    }
    // Merge into the other agents' settings: this page may edit only one.
    final agentConfigs = <String, CLIAgentConfig>{
      ...current.agentConfigs,
      for (final name in widget.agentIds) name: _agents[name]!.toConfig(),
    };
    final updated = current.copyWith(agentConfigs: agentConfigs);
    try {
      await ref.read(configNotifierProvider.notifier).save(updated);
      if (!mounted) return;
      setState(() {
        _saved = true;
        _saving = false;
      });
      _savedResetTimer?.cancel();
      _savedResetTimer = Timer(const Duration(seconds: 3), () {
        if (mounted) setState(() => _saved = false);
      });
    } catch (e) {
      if (mounted) {
        setState(() => _saving = false);
        showToast(context, 'Error: $e', isError: true);
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final configAsync = ref.watch(configNotifierProvider);
    final prompts = ref.watch(agentsProvider).value ?? [];
    final schemas = ref.watch(agentSchemasProvider).value;

    return configAsync.when(
      loading: () => const Center(child: CircularProgressIndicator()),
      error: (_, _) => const Center(child: AppText('Could not load config')),
      data: (config) {
        _initFrom(config);
        return Column(
          children: [
            Expanded(
              child: ListView(
                padding: const EdgeInsets.all(16),
                children: [
                  if (widget.header != null) ...[
                    widget.header!,
                    const SizedBox(height: 12),
                  ],
                  // ── Per-agent sections ───────────────────────────────────
                  for (final name in widget.agentIds) ...[
                    _AgentSection(
                      key: ValueKey('agent-section-$name'),
                      name: name,
                      state: _agents[name]!,
                      schema: schemas?.byName(name),
                      customAgentsApiEnabled:
                          schemas?.customAgentsApiEnabled ?? false,
                      models: _modelsFor(name, schemas?.byName(name)),
                      isProvider: widget.providerIds.contains(name),
                      modelsLoading: widget.modelsLoading,
                      keyConfigured: !widget.providersWithoutKey.contains(name),
                      onReloadModels: widget.onReloadModels,
                      prompts: prompts,
                      onChanged: (s) {
                        setState(() => _agents[name] = s);
                        _markDirty();
                      },
                    ),
                    const SizedBox(height: 12),
                  ],
                ],
              ),
            ),
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 8, 16, 16),
              child: AppSurface(
                elevation: AppSurfaceElevation.canvas,
                padding: const EdgeInsets.all(12),
                child: Row(
                  children: [
                    if (_saving)
                      const SizedBox(
                        width: 16,
                        height: 16,
                        child: CircularProgressIndicator(strokeWidth: 2),
                      )
                    else
                      AppBadge(
                        label: _saved ? 'Saved' : 'Ready',
                        foreground: _saved
                            ? AppColors.success.resolve(context)
                            : AppColors.textMuted.resolve(context),
                        background: _saved
                            ? AppColors.success
                                  .resolve(context)
                                  .withValues(alpha: 0.14)
                            : AppColors.surfaceRaised.resolve(context),
                        border: _saved
                            ? AppColors.success
                                  .resolve(context)
                                  .withValues(alpha: 0.35)
                            : AppColors.border.resolve(context),
                      ),
                    const SizedBox(width: 12),
                    Expanded(
                      child: AppText.muted(
                        _saving
                            ? 'Saving agent settings…'
                            : _hasInvalidInput
                            ? 'Fix the highlighted field to save.'
                            : _saved
                            ? 'Saved'
                            : 'Changes auto-save after edits.',
                      ),
                    ),
                    const SizedBox(width: 12),
                    FilledButton.icon(
                      style: _saved
                          ? FilledButton.styleFrom(
                              backgroundColor: Colors.green.shade700,
                            )
                          : null,
                      icon: _saving
                          ? const SizedBox(
                              width: 16,
                              height: 16,
                              child: CircularProgressIndicator(
                                strokeWidth: 2,
                                color: Colors.white,
                              ),
                            )
                          : Icon(
                              _saved ? Icons.check : Icons.save_outlined,
                              size: 18,
                            ),
                      label: Text(
                        _saving ? 'Saving…' : (_saved ? 'Saved' : 'Save'),
                      ),
                      onPressed: _saving || _hasInvalidInput
                          ? null
                          : () => _save(config),
                    ),
                  ],
                ),
              ),
            ),
          ],
        );
      },
    );
  }
}

// ── Per-agent editable state ────────────────────────────────────────────────

class _AgentState {
  String model = '';
  // UI-only: survives disposal of a lazily built agent section.
  String? unlistedModel;
  int maxTurns = 0;
  String approvalMode = '';
  String extraFlags = '';
  String? promptId;
  // Claude-specific
  String effort = '';
  String permissionMode = '';
  bool bare = false;
  bool dangerouslySkipPerms = false;
  bool noSessionPersistence = false;
  ReviewLimits reviewLimits = const ReviewLimits();
  String executionTimeout = '';
  // Custom agents: their executable (read-only here) and option values.
  String binary = '';
  Map<String, dynamic> options = {};

  _AgentState();

  bool get isCustom => binary.isNotEmpty;

  // UI-only: fields whose typed input is invalid, which blocks saving.
  final Set<String> invalid = {};

  /// The value of a schema field. A built-in agent's fields are its typed
  /// settings; a custom agent's are its options.
  Object? valueOf(String key) {
    if (isCustom) return key == 'model' ? model : options[key];
    return switch (key) {
      'model' => model,
      'max_turns' => maxTurns,
      'approval_mode' => approvalMode,
      'effort' => effort,
      'permission_mode' => permissionMode,
      'bare' => bare,
      'dangerously_skip_perms' => dangerouslySkipPerms,
      'no_session_persistence' => noSessionPersistence,
      _ => null,
    };
  }

  /// Sets a schema field's value; null means "not set".
  void setValue(String key, Object? value) {
    if (isCustom) {
      if (key == 'model') {
        model = (value as String?) ?? '';
      } else if (value == null) {
        options.remove(key);
      } else {
        options[key] = value;
      }
      return;
    }
    switch (key) {
      case 'model':
        model = (value as String?) ?? '';
      case 'max_turns':
        maxTurns = (value as num?)?.toInt() ?? 0;
      case 'approval_mode':
        approvalMode = (value as String?) ?? '';
      case 'effort':
        effort = (value as String?) ?? '';
      case 'permission_mode':
        permissionMode = (value as String?) ?? '';
      case 'bare':
        bare = (value as bool?) ?? false;
      case 'dangerously_skip_perms':
        dangerouslySkipPerms = (value as bool?) ?? false;
      case 'no_session_persistence':
        noSessionPersistence = (value as bool?) ?? false;
    }
  }

  _AgentState.from(CLIAgentConfig ac, {required List<String> modelOptions}) {
    model = ac.model;
    unlistedModel = model.isNotEmpty && !modelOptions.contains(model)
        ? model
        : null;
    maxTurns = ac.maxTurns;
    approvalMode = ac.approvalMode;
    extraFlags = ac.extraFlags;
    promptId = ac.promptId;
    effort = ac.effort;
    permissionMode = ac.permissionMode;
    bare = ac.bare;
    dangerouslySkipPerms = ac.dangerouslySkipPerms;
    noSessionPersistence = ac.noSessionPersistence;
    reviewLimits = ac.reviewLimits;
    executionTimeout = ac.executionTimeout;
    binary = ac.binary;
    options = {...ac.options};
  }

  CLIAgentConfig toConfig() => CLIAgentConfig(
    model: model,
    maxTurns: maxTurns,
    approvalMode: approvalMode,
    extraFlags: extraFlags,
    promptId: promptId,
    effort: effort,
    permissionMode: permissionMode,
    bare: bare,
    dangerouslySkipPerms: dangerouslySkipPerms,
    noSessionPersistence: noSessionPersistence,
    reviewLimits: reviewLimits,
    executionTimeout: executionTimeout,
    binary: binary,
    options: Map<String, dynamic>.unmodifiable(options),
  );

  bool get hasOverrides => toConfig().hasConfig;
}

// ── Agent section card ──────────────────────────────────────────────────────

class _AgentSection extends StatefulWidget {
  final String name;
  final _AgentState state;

  /// The agent's options schema; null while it loads or on a daemon without
  /// schemas, in which case only the shared settings are shown.
  final AgentSchema? schema;

  /// Whether config.toml lets the app change a custom agent's extra flags.
  final bool customAgentsApiEnabled;
  final List<String> models;
  final bool isProvider;
  final bool modelsLoading;
  final bool keyConfigured;
  final Future<void> Function()? onReloadModels;
  final List<dynamic> prompts;
  final ValueChanged<_AgentState> onChanged;

  const _AgentSection({
    super.key,
    required this.name,
    required this.state,
    this.schema,
    this.customAgentsApiEnabled = false,
    required this.models,
    this.isProvider = false,
    this.modelsLoading = false,
    this.keyConfigured = true,
    this.onReloadModels,
    required this.prompts,
    required this.onChanged,
  });

  @override
  State<_AgentSection> createState() => _AgentSectionState();
}

class _AgentSectionState extends State<_AgentSection> {
  late List<String>
  _flags; // each element = one flag entry (may contain spaces)
  final _newFlagCtrl = TextEditingController();
  int? _editingIndex; // index of chip being edited (null = none)
  final _editCtrl = TextEditingController();

  @override
  void initState() {
    super.initState();
    _flags = _parseFlags(widget.state.extraFlags);
  }

  @override
  void dispose() {
    _newFlagCtrl.dispose();
    _editCtrl.dispose();
    super.dispose();
  }

  /// Parses "extraFlags" string into individual flag entries.
  /// Groups `--flag [value]` together: splits on ` --` or ` -` boundaries.
  static List<String> _parseFlags(String raw) {
    final trimmed = raw.trim();
    if (trimmed.isEmpty) return [];
    // Split before each flag that starts with - or --
    final parts = trimmed.split(RegExp(r'\s+(?=--?[a-zA-Z])'));
    return parts.where((p) => p.trim().isNotEmpty).toList();
  }

  void _commit() {
    widget.state.extraFlags = _flags.join(' ');
    widget.onChanged(widget.state);
  }

  void _addFlag() {
    final flag = _newFlagCtrl.text.trim();
    if (flag.isEmpty) return;
    setState(() {
      _flags.add(flag);
      _newFlagCtrl.clear();
    });
    _commit();
  }

  void _removeFlag(int idx) {
    setState(() => _flags.removeAt(idx));
    _commit();
  }

  void _startEdit(int idx) {
    setState(() {
      _editingIndex = idx;
      _editCtrl.text = _flags[idx];
    });
  }

  void _confirmEdit() {
    final val = _editCtrl.text.trim();
    if (val.isNotEmpty && _editingIndex != null) {
      setState(() {
        _flags[_editingIndex!] = val;
        _editingIndex = null;
      });
      _commit();
    }
  }

  void _cancelEdit() => setState(() => _editingIndex = null);

  @override
  Widget build(BuildContext context) {
    final name = widget.name;
    final s = widget.state;
    final models = widget.models;
    final preservedUnlistedModel = s.unlistedModel;
    final unlistedModel =
        preservedUnlistedModel != null &&
            !models.contains(preservedUnlistedModel)
        ? preservedUnlistedModel
        : null;

    final schema = widget.schema;
    final modelField = schema?.field('model');
    // Built-in agents always take a model; a custom agent only when its
    // schema declares one.
    final showModel = !s.isCustom || modelField != null;
    final optionFields = [
      for (final f in schema?.fields ?? const <AgentSchemaField>[])
        if (f.key != 'model') f,
    ];
    final modelEmptyLabel = modelField?.emptyLabel.isNotEmpty == true
        ? modelField!.emptyLabel
        : 'CLI default';
    final needsModel = widget.isProvider && s.model.trim().isEmpty;
    final accent = needsModel
        ? AppColors.warning.resolve(context)
        : _cliColor(context, name);
    return AppSurface(
      padding: const EdgeInsets.all(16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Text(_cliEmoji(name), style: const TextStyle(fontSize: 20)),
              const SizedBox(width: 8),
              AppText.sectionTitle(name),
              const SizedBox(width: 8),
              AppBadge(
                label: needsModel
                    ? 'Choose a model'
                    : s.hasOverrides
                    ? 'Configured'
                    : 'CLI default',
                foreground: accent,
                background: accent.withValues(alpha: 0.14),
                border: accent.withValues(alpha: 0.35),
              ),
            ],
          ),
          const SizedBox(height: 12),

          if (showModel)
            Row(
              children: [
                Expanded(
                  child: widget.isProvider
                      ? ProviderModelField(
                          key: ValueKey('provider-model-field-$name'),
                          fieldKey: ValueKey('model-$name'),
                          model: s.model,
                          models: models,
                          loading: widget.modelsLoading,
                          keyConfigured: widget.keyConfigured,
                          onRetry: widget.onReloadModels,
                          onChanged: (v) {
                            setState(() => s.model = v);
                            widget.onChanged(s);
                          },
                        )
                      : DropdownButtonFormField<String>(
                          key: ValueKey('model-$name'),
                          // ignore: deprecated_member_use
                          value: s.model.isEmpty ? null : s.model,
                          decoration: InputDecoration(
                            labelText: modelField?.label ?? 'Model',
                            border: const OutlineInputBorder(),
                          ),
                          items: [
                            DropdownMenuItem<String>(
                              value: null,
                              child: Text(modelEmptyLabel),
                            ),
                            if (unlistedModel != null)
                              DropdownMenuItem(
                                value: unlistedModel,
                                child: Text('$unlistedModel (not listed)'),
                              ),
                            ...models.map(
                              (m) => DropdownMenuItem(value: m, child: Text(m)),
                            ),
                          ],
                          onChanged: (v) {
                            setState(() => s.model = v ?? '');
                            widget.onChanged(s);
                          },
                        ),
                ),
              ],
            ),
          const SizedBox(height: 10),

          _promptDropdown(s),
          const SizedBox(height: 12),

          if (schema != null && schema.description.isNotEmpty) ...[
            Text(
              schema.description,
              style: Theme.of(context).textTheme.bodySmall,
            ),
            const SizedBox(height: 12),
          ],
          if (optionFields.isNotEmpty) ...[
            AppSurface(
              elevation: AppSurfaceElevation.canvas,
              padding: const EdgeInsets.all(12),
              child: AgentSchemaForm(
                key: ValueKey('agent-schema-form-$name'),
                keyPrefix: name,
                fields: optionFields,
                defaultEmptyLabel: widget.isProvider
                    ? 'Model default'
                    : 'CLI default',
                valueOf: s.valueOf,
                // A built-in agent's numeric settings are integers (its
                // typed config fields); a custom agent's may be decimals.
                integerNumbers: !s.isCustom,
                onChanged: (key, value) {
                  setState(() => s.setValue(key, value));
                  widget.onChanged(s);
                },
                onInvalidChanged: (key, invalid) {
                  setState(() {
                    if (invalid) {
                      s.invalid.add(key);
                    } else {
                      s.invalid.remove(key);
                    }
                  });
                  widget.onChanged(s);
                },
              ),
            ),
            const SizedBox(height: 12),
          ],

          AppText.label(
            'Review limits',
            color: Theme.of(context).colorScheme.onSurfaceVariant,
          ),
          const SizedBox(height: 4),
          Text(
            'Reviews this agent may run. When it is spent, the review goes to '
            'the fallback agent, or waits for a free slot.',
            style: Theme.of(context).textTheme.bodySmall,
          ),
          const SizedBox(height: 8),
          ReviewLimitsFields(
            keyPrefix: 'agent-$name',
            value: s.reviewLimits,
            onChanged: (v) {
              setState(() => s.reviewLimits = v);
              widget.onChanged(s);
            },
          ),
          const SizedBox(height: 12),

          AppText.label(
            'Execution flags',
            color: Theme.of(context).colorScheme.onSurfaceVariant,
          ),
          const SizedBox(height: 8),
          if (s.isCustom && !widget.customAgentsApiEnabled)
            _customFlagsLocked(context)
          else
            ..._flagsEditor(name),
        ],
      ),
    );
  }

  /// A custom agent's extra flags reach its CLI unchecked, so the daemon only
  /// accepts them from config.toml unless it opts in to the app managing
  /// custom agents.
  Widget _customFlagsLocked(BuildContext context) {
    final flags = widget.state.extraFlags;
    return AppSurface(
      key: ValueKey('custom-flags-locked-${widget.name}'),
      elevation: AppSurfaceElevation.canvas,
      padding: const EdgeInsets.all(12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (flags.isNotEmpty) ...[
            SelectableText(
              flags,
              style: const TextStyle(fontSize: 12, fontFamily: 'monospace'),
            ),
            const SizedBox(height: 6),
          ],
          const AppText.muted(
            'A custom agent\'s extra flags are set in config.toml '
            '(extra_flags under its [ai.agents] section), unless '
            '[ai] allow_custom_agents_api = true lets this app manage them.',
          ),
        ],
      ),
    );
  }

  List<Widget> _flagsEditor(String name) {
    return [
      if (_flags.isNotEmpty)
        Wrap(
          spacing: 6,
          runSpacing: 6,
          children: List.generate(_flags.length, (idx) {
            if (_editingIndex == idx) {
              return SizedBox(
                width: 260,
                child: Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    Expanded(
                      child: TextFormField(
                        controller: _editCtrl,
                        autofocus: true,
                        decoration: const InputDecoration(
                          border: OutlineInputBorder(),
                          isDense: true,
                          contentPadding: EdgeInsets.symmetric(
                            horizontal: 8,
                            vertical: 6,
                          ),
                        ),
                        onFieldSubmitted: (_) => _confirmEdit(),
                      ),
                    ),
                    const SizedBox(width: 4),
                    IconButton(
                      icon: const Icon(Icons.check, size: 16),
                      onPressed: _confirmEdit,
                      visualDensity: VisualDensity.compact,
                      padding: EdgeInsets.zero,
                    ),
                    IconButton(
                      icon: const Icon(Icons.close, size: 16),
                      onPressed: _cancelEdit,
                      visualDensity: VisualDensity.compact,
                      padding: EdgeInsets.zero,
                    ),
                  ],
                ),
              );
            }
            return InputChip(
              label: Text(
                _flags[idx],
                style: const TextStyle(fontSize: 12, fontFamily: 'monospace'),
              ),
              deleteIcon: const Icon(Icons.close, size: 14),
              onDeleted: () => _removeFlag(idx),
              onPressed: () => _startEdit(idx),
              labelPadding: const EdgeInsets.symmetric(horizontal: 4),
              materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
              tooltip: 'Tap to edit',
            );
          }),
        )
      else
        AppSurface(
          elevation: AppSurfaceElevation.canvas,
          padding: const EdgeInsets.all(12),
          child: const AppText.muted('No extra execution flags'),
        ),

      if (_flags.isNotEmpty) const SizedBox(height: 8),

      Row(
        children: [
          Expanded(
            child: TextFormField(
              controller: _newFlagCtrl,
              decoration: InputDecoration(
                hintText: _extraFlagsHint(name),
                border: const OutlineInputBorder(),
                isDense: true,
                contentPadding: const EdgeInsets.symmetric(
                  horizontal: 12,
                  vertical: 10,
                ),
              ),
              onFieldSubmitted: (_) => _addFlag(),
            ),
          ),
          const SizedBox(width: 8),
          AppButton.secondary(label: 'Add', onPressed: _addFlag),
        ],
      ),
      const SizedBox(height: 8),
    ];
  }

  Widget _promptDropdown(_AgentState s) {
    return DropdownButtonFormField<String?>(
      // ignore: deprecated_member_use
      value: s.promptId,
      decoration: const InputDecoration(
        labelText: 'Default prompt',
        helperText: 'Overrides global; repo config overrides this',
        border: OutlineInputBorder(),
      ),
      items: [
        const DropdownMenuItem<String?>(
          value: null,
          child: Text('Global active'),
        ),
        ...widget.prompts.map(
          (p) => DropdownMenuItem<String?>(
            value: p.id as String,
            child: Text(p.name as String),
          ),
        ),
      ],
      onChanged: (v) {
        setState(() => s.promptId = v);
        widget.onChanged(s);
      },
    );
  }

  String _cliEmoji(String name) {
    switch (name) {
      case 'claude':
        return '🔷';
      case 'gemini':
        return '🟡';
      case 'codex':
        return '🟢';
      case 'copilot':
        return '🐙';
      case 'cursor_cli':
        return '🖱️';
      case 'opencode':
        return '⬛';
      case 'openrouter':
        return '🔀';
      default:
        return '🤖';
    }
  }

  String _extraFlagsHint(String name) {
    switch (name) {
      case 'claude':
        return '--allowedTools Bash,Read';
      case 'gemini':
        return '--all-files';
      case 'codex':
        return '--sandbox workspace-write';
      case 'copilot':
        return '--excluded-tools fetch';
      case 'opencode':
        return '--variant high';
      default:
        return '--flag value';
    }
  }
}

Color _cliColor(BuildContext context, String name) {
  switch (name) {
    case 'claude':
      return AppColors.accent.resolve(context);
    case 'gemini':
      return AppColors.warning.resolve(context);
    case 'codex':
      return AppColors.success.resolve(context);
    case 'copilot':
    case 'cursor_cli':
      return AppColors.accent.resolve(context);
    default:
      return AppColors.textMuted.resolve(context);
  }
}
