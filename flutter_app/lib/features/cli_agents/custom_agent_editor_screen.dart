import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../core/models/agent_schema.dart';
import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/toast.dart';
import '../dashboard/dashboard_providers.dart';
import 'agent_catalog_screen.dart' show cliAgentCatalogProvider;
import 'agent_schema_form.dart';
import 'agent_schemas_provider.dart';

/// Names a custom agent can never take: the built-in agents and the other
/// catalog entries.
Set<String> _reservedNames(AgentSchemaCatalog? catalog) => {
  'cursor',
  for (final s in catalog?.schemas ?? const <AgentSchema>[])
    if (s.builtin) s.name,
};

/// Creates or edits a custom agent: a visual editor of its schema, a live
/// preview of the settings form it produces, and the raw JSON. It saves
/// through PUT /agent-schemas when config.toml opts in; otherwise it gives
/// the operator the JSON schema and the config.toml section to add by hand.
class CustomAgentEditorScreen extends ConsumerStatefulWidget {
  /// The agent to edit; null creates a new one.
  final String? agentName;

  const CustomAgentEditorScreen({super.key, this.agentName});

  @override
  ConsumerState<CustomAgentEditorScreen> createState() =>
      _CustomAgentEditorScreenState();
}

class _CustomAgentEditorScreenState
    extends ConsumerState<CustomAgentEditorScreen> {
  AgentSchema _draft = const AgentSchema(name: '');
  bool _initialized = false;
  bool _saving = false;
  final Map<String, dynamic> _previewValues = {};
  final _jsonCtrl = TextEditingController();
  String? _jsonError;
  // Rebuilds the visual editor's fields after the JSON tab replaced them.
  int _generation = 0;

  bool get _isNew => widget.agentName == null;

  @override
  void dispose() {
    _jsonCtrl.dispose();
    super.dispose();
  }

  void _init(AgentSchemaCatalog? catalog) {
    if (_initialized) return;
    if (!_isNew) {
      if (catalog == null) return;
      final existing = catalog.byName(widget.agentName!);
      if (existing != null) _draft = existing;
    }
    _initialized = true;
    _jsonCtrl.text = _draft.toPrettyJson();
  }

  void _update(AgentSchema next) {
    setState(() {
      _draft = next;
      _jsonCtrl.text = next.toPrettyJson();
      _jsonError = null;
    });
  }

  void _updateField(int index, AgentSchemaField field) {
    final fields = [..._draft.fields]..[index] = field;
    _update(_draft.copyWith(fields: fields));
  }

  void _applyJson() {
    try {
      final decoded = jsonDecode(_jsonCtrl.text);
      if (decoded is! Map<String, dynamic>) {
        throw const FormatException('the schema must be a JSON object');
      }
      var next = AgentSchema.fromJson(decoded);
      // Editing keeps the agent's name: renaming is a new agent.
      if (!_isNew) next = next.copyWith(name: _draft.name);
      setState(() {
        _draft = next;
        _jsonCtrl.text = next.toPrettyJson();
        _jsonError = null;
        _generation++;
      });
    } on FormatException catch (e) {
      setState(() => _jsonError = 'Invalid JSON: ${e.message}');
    }
  }

  Future<void> _save() async {
    setState(() => _saving = true);
    final container = ProviderScope.containerOf(context, listen: false);
    try {
      final api = container.read(apiClientProvider);
      await api.putAgentSchema(_draft);
      // The daemon reloads its config on save; rescan so the catalog lists
      // the agent (or its new binary) right away.
      await api.rescanCliAgents();
      container.invalidate(agentSchemasProvider);
      container.invalidate(cliAgentCatalogProvider);
      if (!mounted) return;
      showToast(context, 'Saved ${_draft.name}');
      context.go('/cli-agents/${_draft.name}');
    } catch (e) {
      if (mounted) showToast(context, '$e', isError: true);
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  Future<void> _delete() async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text('Delete ${_draft.name}?'),
        content: const Text(
          'The agent and its settings are removed from config.toml.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            key: const ValueKey('custom-agent-confirm-delete'),
            onPressed: () => Navigator.pop(context, true),
            child: const Text('Delete'),
          ),
        ],
      ),
    );
    if (confirmed != true || !mounted) return;
    final container = ProviderScope.containerOf(context, listen: false);
    try {
      await container.read(apiClientProvider).deleteAgentSchema(_draft.name);
      container.invalidate(agentSchemasProvider);
      container.invalidate(cliAgentCatalogProvider);
      if (!mounted) return;
      showToast(context, 'Deleted ${_draft.name}');
      context.go('/cli-agents');
    } catch (e) {
      if (mounted) showToast(context, '$e', isError: true);
    }
  }

  Future<void> _copy(String text, String what) async {
    await Clipboard.setData(ClipboardData(text: text));
    if (mounted) showToast(context, 'Copied the $what');
  }

  @override
  Widget build(BuildContext context) {
    final schemasAsync = ref.watch(agentSchemasProvider);
    final catalog = schemasAsync.value;
    _init(catalog);
    if (!_initialized) {
      return schemasAsync.hasError
          ? const Center(child: AppText('Could not load the agent schemas'))
          : const Center(child: CircularProgressIndicator());
    }
    final apiEnabled = catalog?.customAgentsApiEnabled ?? false;
    final errors = validateCustomSchema(
      _draft,
      builtinNames: _reservedNames(catalog),
    );
    return DefaultTabController(
      length: 3,
      child: Column(
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(8, 8, 16, 0),
            child: Row(
              children: [
                IconButton(
                  key: const ValueKey('custom-agent-back'),
                  tooltip: 'All agents',
                  icon: const Icon(Icons.arrow_back),
                  onPressed: () => context.go('/cli-agents'),
                ),
                const Text('🤖', style: TextStyle(fontSize: 20)),
                const SizedBox(width: 8),
                Expanded(
                  child: AppText.sectionTitle(
                    _isNew ? 'New custom agent' : 'Edit ${_draft.title}',
                  ),
                ),
              ],
            ),
          ),
          const TabBar(
            tabs: [
              Tab(key: ValueKey('custom-agent-tab-fields'), text: 'Fields'),
              Tab(key: ValueKey('custom-agent-tab-preview'), text: 'Preview'),
              Tab(key: ValueKey('custom-agent-tab-json'), text: 'JSON'),
            ],
          ),
          Expanded(
            child: TabBarView(
              children: [_fieldsTab(), _previewTab(), _jsonTab()],
            ),
          ),
          _footer(apiEnabled, errors),
        ],
      ),
    );
  }

  Widget _fieldsTab() {
    return ListView(
      key: ValueKey('custom-agent-fields-$_generation'),
      padding: const EdgeInsets.all(16),
      children: [
        AppFieldGrid(
          minFieldWidth: 240,
          children: [
            TextFormField(
              key: const ValueKey('custom-agent-name'),
              initialValue: _draft.name,
              enabled: _isNew,
              decoration: const InputDecoration(
                labelText: 'Name',
                helperText: 'The agent id: lowercase, e.g. aider',
                border: OutlineInputBorder(),
              ),
              onChanged: (v) => _update(_draft.copyWith(name: v.trim())),
            ),
            TextFormField(
              key: const ValueKey('custom-agent-display-name'),
              initialValue: _draft.displayName,
              decoration: const InputDecoration(
                labelText: 'Display name',
                border: OutlineInputBorder(),
              ),
              onChanged: (v) => _update(_draft.copyWith(displayName: v)),
            ),
            TextFormField(
              key: const ValueKey('custom-agent-binary'),
              initialValue: _draft.binary,
              decoration: const InputDecoration(
                labelText: 'Binary',
                helperText: 'An executable on the daemon\'s PATH, no path',
                border: OutlineInputBorder(),
              ),
              onChanged: (v) => _update(_draft.copyWith(binary: v.trim())),
            ),
            TextFormField(
              key: const ValueKey('custom-agent-icon-color'),
              initialValue: _draft.iconColor,
              decoration: const InputDecoration(
                labelText: 'Icon colour',
                hintText: '#6B7FFF',
                border: OutlineInputBorder(),
              ),
              onChanged: (v) => _update(_draft.copyWith(iconColor: v.trim())),
            ),
          ],
        ),
        const SizedBox(height: 12),
        TextFormField(
          key: const ValueKey('custom-agent-description'),
          initialValue: _draft.description,
          maxLines: 2,
          decoration: const InputDecoration(
            labelText: 'Description',
            border: OutlineInputBorder(),
          ),
          onChanged: (v) => _update(_draft.copyWith(description: v)),
        ),
        const SizedBox(height: 16),
        AppText.label('Options'),
        const SizedBox(height: 8),
        for (final (i, f) in _draft.fields.indexed) ...[
          _FieldEditor(
            key: ValueKey('custom-agent-field-$_generation-$i'),
            index: i,
            field: f,
            isFirst: i == 0,
            isLast: i == _draft.fields.length - 1,
            onChanged: (next) => _updateField(i, next),
            onMove: (delta) {
              final fields = [..._draft.fields];
              final moved = fields.removeAt(i);
              fields.insert(i + delta, moved);
              _update(_draft.copyWith(fields: fields));
              setState(() => _generation++);
            },
            onRemove: () {
              final fields = [..._draft.fields]..removeAt(i);
              _update(_draft.copyWith(fields: fields));
              setState(() => _generation++);
            },
          ),
          const SizedBox(height: 8),
        ],
        Align(
          alignment: Alignment.centerLeft,
          child: AppButton.secondary(
            key: const ValueKey('custom-agent-add-field'),
            label: 'Add option',
            onPressed: () => _update(
              _draft.copyWith(
                fields: [
                  ..._draft.fields,
                  const AgentSchemaField(
                    key: '',
                    type: AgentFieldType.text,
                    label: '',
                  ),
                ],
              ),
            ),
          ),
        ),
      ],
    );
  }

  Widget _previewTab() {
    return ListView(
      padding: const EdgeInsets.all(16),
      children: [
        AppText.muted(
          'The settings form of ${_draft.title.isEmpty ? 'this agent' : _draft.title}, '
          'as the agent page will show it. Changes here are not saved.',
        ),
        const SizedBox(height: 12),
        AppSurface(
          elevation: AppSurfaceElevation.canvas,
          padding: const EdgeInsets.all(12),
          child: _draft.fields.isEmpty
              ? const AppText.muted('No options yet.')
              : AgentSchemaForm(
                  key: ValueKey('custom-agent-preview-$_generation'),
                  keyPrefix: 'preview',
                  fields: _draft.fields,
                  valueOf: (k) => _previewValues[k],
                  onChanged: (k, v) => setState(() {
                    if (v == null) {
                      _previewValues.remove(k);
                    } else {
                      _previewValues[k] = v;
                    }
                  }),
                ),
        ),
      ],
    );
  }

  Widget _jsonTab() {
    return Padding(
      padding: const EdgeInsets.all(16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Expanded(
            child: TextField(
              key: const ValueKey('custom-agent-json'),
              controller: _jsonCtrl,
              maxLines: null,
              expands: true,
              style: const TextStyle(fontFamily: 'monospace', fontSize: 12),
              decoration: InputDecoration(
                border: const OutlineInputBorder(),
                errorText: _jsonError,
              ),
            ),
          ),
          const SizedBox(height: 8),
          Align(
            alignment: Alignment.centerRight,
            child: AppButton.secondary(
              key: const ValueKey('custom-agent-apply-json'),
              label: 'Apply JSON',
              onPressed: _applyJson,
            ),
          ),
        ],
      ),
    );
  }

  Widget _footer(bool apiEnabled, List<String> errors) {
    final valid = errors.isEmpty;
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 8, 16, 16),
      child: AppSurface(
        elevation: AppSurfaceElevation.canvas,
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            if (!valid)
              for (final e in errors.take(4))
                Text(
                  e,
                  style: TextStyle(
                    color: AppColors.warning.resolve(context),
                    fontSize: 12,
                  ),
                ),
            if (!apiEnabled) ...[
              if (!valid) const SizedBox(height: 8),
              const AppText.muted(
                key: ValueKey('custom-agent-api-disabled'),
                'Custom agents run an executable of your choosing, so they are '
                'defined in config.toml. Copy the section below into it — or '
                'set [ai] allow_custom_agents_api = true there to save from '
                'this screen.',
              ),
            ],
            const SizedBox(height: 8),
            Wrap(
              spacing: 8,
              runSpacing: 8,
              children: [
                if (apiEnabled) ...[
                  AppButton(
                    key: const ValueKey('custom-agent-save'),
                    label: _saving ? 'Saving…' : 'Save',
                    onPressed: valid && !_saving ? _save : null,
                  ),
                  if (!_isNew)
                    AppButton.destructive(
                      key: const ValueKey('custom-agent-delete'),
                      label: 'Delete',
                      onPressed: _saving ? null : _delete,
                    ),
                ] else ...[
                  AppButton(
                    key: const ValueKey('custom-agent-copy-toml'),
                    label: 'Copy config.toml section',
                    onPressed: valid
                        ? () => _copy(
                            _draft.toTomlSnippet(),
                            'config.toml section',
                          )
                        : null,
                  ),
                  AppButton.secondary(
                    key: const ValueKey('custom-agent-copy-json'),
                    label: 'Copy JSON schema',
                    onPressed: valid
                        ? () => _copy(_draft.toPrettyJson(), 'JSON schema')
                        : null,
                  ),
                ],
              ],
            ),
          ],
        ),
      ),
    );
  }
}

/// Edits one option of the schema.
class _FieldEditor extends StatelessWidget {
  final int index;
  final AgentSchemaField field;
  final bool isFirst;
  final bool isLast;
  final ValueChanged<AgentSchemaField> onChanged;
  final ValueChanged<int> onMove;
  final VoidCallback onRemove;

  const _FieldEditor({
    super.key,
    required this.index,
    required this.field,
    required this.isFirst,
    required this.isLast,
    required this.onChanged,
    required this.onMove,
    required this.onRemove,
  });

  InputDecoration _deco(String label, {String? helper}) => InputDecoration(
    labelText: label,
    helperText: helper,
    border: const OutlineInputBorder(),
    isDense: true,
  );

  String _k(String part) => 'custom-agent-field-$index-$part';

  @override
  Widget build(BuildContext context) {
    final f = field;
    return AppSurface(
      padding: const EdgeInsets.all(12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: AppText.label(
                  f.label.isEmpty ? 'Option ${index + 1}' : f.label,
                ),
              ),
              IconButton(
                key: ValueKey(_k('up')),
                tooltip: 'Move up',
                icon: const Icon(Icons.arrow_upward, size: 18),
                onPressed: isFirst ? null : () => onMove(-1),
              ),
              IconButton(
                key: ValueKey(_k('down')),
                tooltip: 'Move down',
                icon: const Icon(Icons.arrow_downward, size: 18),
                onPressed: isLast ? null : () => onMove(1),
              ),
              IconButton(
                key: ValueKey(_k('remove')),
                tooltip: 'Remove',
                icon: const Icon(Icons.delete_outline, size: 18),
                onPressed: onRemove,
              ),
            ],
          ),
          const SizedBox(height: 8),
          AppFieldGrid(
            minFieldWidth: 200,
            children: [
              TextFormField(
                key: ValueKey(_k('key')),
                initialValue: f.key,
                decoration: _deco('Key', helper: 'Stored as <key> = …'),
                onChanged: (v) => onChanged(f.copyWith(key: v.trim())),
              ),
              DropdownButtonFormField<AgentFieldType>(
                key: ValueKey(_k('type')),
                // A type this app does not know (typed in the JSON tab)
                // shows as unset until one is picked.
                // ignore: deprecated_member_use
                value: f.type == AgentFieldType.unknown ? null : f.type,
                decoration: _deco('Type'),
                items: const [
                  DropdownMenuItem(
                    value: AgentFieldType.select,
                    child: Text('Dropdown'),
                  ),
                  DropdownMenuItem(
                    value: AgentFieldType.bool,
                    child: Text('Toggle'),
                  ),
                  DropdownMenuItem(
                    value: AgentFieldType.number,
                    child: Text('Number'),
                  ),
                  DropdownMenuItem(
                    value: AgentFieldType.text,
                    child: Text('Text'),
                  ),
                ],
                onChanged: (t) {
                  if (t == null || t == f.type) return;
                  // Attributes of the old type do not carry over.
                  onChanged(
                    AgentSchemaField(
                      key: f.key,
                      type: t,
                      label: f.label,
                      description: f.description,
                      flag: f.flag,
                    ),
                  );
                },
              ),
              TextFormField(
                key: ValueKey(_k('label')),
                initialValue: f.label,
                decoration: _deco('Label'),
                onChanged: (v) => onChanged(f.copyWith(label: v)),
              ),
              TextFormField(
                key: ValueKey(_k('flag')),
                initialValue: f.flag,
                decoration: _deco('Flag', helper: 'e.g. --model'),
                onChanged: (v) => onChanged(f.copyWith(flag: v.trim())),
              ),
              ..._typeSpecific(),
            ],
          ),
          const SizedBox(height: 8),
          TextFormField(
            key: ValueKey(_k('description')),
            initialValue: f.description,
            decoration: _deco('Help text'),
            onChanged: (v) => onChanged(f.copyWith(description: v)),
          ),
        ],
      ),
    );
  }

  List<Widget> _typeSpecific() {
    final f = field;
    switch (f.type) {
      case AgentFieldType.select:
        return [
          TextFormField(
            key: ValueKey(_k('options')),
            initialValue: f.options.join(', '),
            decoration: _deco('Options', helper: 'Comma-separated'),
            onChanged: (v) => onChanged(
              f.copyWith(
                options: [
                  for (final o in v.split(','))
                    if (o.trim().isNotEmpty) o.trim(),
                ],
              ),
            ),
          ),
          TextFormField(
            key: ValueKey(_k('default')),
            initialValue: f.defaultValue is String
                ? f.defaultValue as String
                : '',
            decoration: _deco('Default'),
            onChanged: (v) => onChanged(
              f.copyWith(defaultValue: v.trim().isEmpty ? null : v.trim()),
            ),
          ),
          TextFormField(
            key: ValueKey(_k('empty-label')),
            initialValue: f.emptyLabel,
            decoration: _deco('Empty choice', helper: 'e.g. CLI default'),
            onChanged: (v) => onChanged(f.copyWith(emptyLabel: v)),
          ),
          CheckboxListTile(
            key: ValueKey(_k('allow-custom')),
            value: f.allowCustom,
            dense: true,
            contentPadding: EdgeInsets.zero,
            title: const Text('Allow any value'),
            onChanged: (v) => onChanged(f.copyWith(allowCustom: v ?? false)),
          ),
        ];
      case AgentFieldType.bool:
        return [
          TextFormField(
            key: ValueKey(_k('false-flag')),
            initialValue: f.falseFlag,
            decoration: _deco(
              'Flag when off',
              helper: 'e.g. --no-auto-commits',
            ),
            onChanged: (v) => onChanged(f.copyWith(falseFlag: v.trim())),
          ),
          CheckboxListTile(
            key: ValueKey(_k('default-on')),
            value: f.defaultValue == true,
            dense: true,
            contentPadding: EdgeInsets.zero,
            title: const Text('On by default'),
            onChanged: (v) =>
                onChanged(f.copyWith(defaultValue: (v ?? false) ? true : null)),
          ),
          CheckboxListTile(
            key: ValueKey(_k('warning')),
            value: f.warning,
            dense: true,
            contentPadding: EdgeInsets.zero,
            title: const Text('Dangerous (only config.toml turns it on)'),
            onChanged: (v) => onChanged(f.copyWith(warning: v ?? false)),
          ),
        ];
      case AgentFieldType.number:
        num? parse(String v) =>
            v.trim().isEmpty ? null : num.tryParse(v.trim());
        return [
          TextFormField(
            key: ValueKey(_k('min')),
            initialValue: f.min?.toString() ?? '',
            decoration: _deco('Min'),
            keyboardType: TextInputType.number,
            onChanged: (v) => onChanged(f.copyWith(min: parse(v))),
          ),
          TextFormField(
            key: ValueKey(_k('max')),
            initialValue: f.max?.toString() ?? '',
            decoration: _deco('Max'),
            keyboardType: TextInputType.number,
            onChanged: (v) => onChanged(f.copyWith(max: parse(v))),
          ),
          TextFormField(
            key: ValueKey(_k('default')),
            initialValue: f.defaultValue?.toString() ?? '',
            decoration: _deco('Default'),
            keyboardType: TextInputType.number,
            onChanged: (v) => onChanged(f.copyWith(defaultValue: parse(v))),
          ),
        ];
      case AgentFieldType.unknown:
        return const [];
      case AgentFieldType.text:
        return [
          TextFormField(
            key: ValueKey(_k('default')),
            initialValue: f.defaultValue is String
                ? f.defaultValue as String
                : '',
            decoration: _deco('Default'),
            onChanged: (v) => onChanged(
              f.copyWith(defaultValue: v.trim().isEmpty ? null : v.trim()),
            ),
          ),
        ];
    }
  }
}
