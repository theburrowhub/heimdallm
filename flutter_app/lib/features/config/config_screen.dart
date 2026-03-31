import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/models/config_model.dart';
import '../../shared/widgets/toast.dart';
import 'config_providers.dart';

class ConfigScreen extends ConsumerStatefulWidget {
  const ConfigScreen({super.key});

  @override
  ConsumerState<ConfigScreen> createState() => _ConfigScreenState();
}

class _ConfigScreenState extends ConsumerState<ConfigScreen> {
  final _reposController = TextEditingController();
  String _pollInterval = '5m';
  String _aiPrimary = 'claude';
  String _aiFallback = '';
  int _retentionDays = 90;
  bool _initialized = false;

  @override
  void dispose() {
    _reposController.dispose();
    super.dispose();
  }

  void _initFrom(AppConfig config) {
    if (_initialized) return;
    _initialized = true;
    _reposController.text = config.repositories.join(', ');
    _pollInterval = config.pollInterval;
    _aiPrimary = config.aiPrimary;
    _aiFallback = config.aiFallback;
    _retentionDays = config.retentionDays;
  }

  @override
  Widget build(BuildContext context) {
    final configAsync = ref.watch(configNotifierProvider);

    return Scaffold(
      appBar: AppBar(title: const Text('Configuration')),
      body: configAsync.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (e, _) => Center(child: Text('Error: $e')),
        data: (config) {
          _initFrom(config);
          return SingleChildScrollView(
            padding: const EdgeInsets.all(24),
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 600),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  _section('GitHub'),
                  TextFormField(
                    controller: _reposController,
                    decoration: const InputDecoration(
                      labelText: 'Repositories (comma-separated)',
                      hintText: 'org/repo1, org/repo2',
                      border: OutlineInputBorder(),
                    ),
                  ),
                  const SizedBox(height: 16),
                  _section('Polling'),
                  DropdownButtonFormField<String>(
                    value: _pollInterval,
                    decoration: const InputDecoration(
                      labelText: 'Poll Interval',
                      border: OutlineInputBorder(),
                    ),
                    items: ['1m', '5m', '30m', '1h']
                        .map((v) => DropdownMenuItem(value: v, child: Text(v)))
                        .toList(),
                    onChanged: (v) => setState(() => _pollInterval = v!),
                  ),
                  const SizedBox(height: 16),
                  _section('AI'),
                  DropdownButtonFormField<String>(
                    value: _aiPrimary,
                    decoration: const InputDecoration(
                      labelText: 'Primary AI',
                      border: OutlineInputBorder(),
                    ),
                    items: ['claude', 'gemini', 'codex']
                        .map((v) => DropdownMenuItem(value: v, child: Text(v)))
                        .toList(),
                    onChanged: (v) => setState(() => _aiPrimary = v!),
                  ),
                  const SizedBox(height: 12),
                  DropdownButtonFormField<String>(
                    value: _aiFallback.isEmpty ? null : _aiFallback,
                    decoration: const InputDecoration(
                      labelText: 'Fallback AI (optional)',
                      border: OutlineInputBorder(),
                    ),
                    items: [
                      const DropdownMenuItem<String>(value: null, child: Text('None')),
                      ...['claude', 'gemini', 'codex']
                          .map((v) => DropdownMenuItem<String>(value: v, child: Text(v))),
                    ],
                    onChanged: (v) => setState(() => _aiFallback = v ?? ''),
                  ),
                  const SizedBox(height: 16),
                  _section('Retention'),
                  TextFormField(
                    initialValue: _retentionDays.toString(),
                    decoration: const InputDecoration(
                      labelText: 'Keep reviews for (days, 0 = forever)',
                      border: OutlineInputBorder(),
                    ),
                    keyboardType: TextInputType.number,
                    onChanged: (v) => _retentionDays = int.tryParse(v) ?? 90,
                  ),
                  const SizedBox(height: 24),
                  SizedBox(
                    width: double.infinity,
                    child: ElevatedButton(
                      onPressed: () async {
                        final repos = _reposController.text
                            .split(',')
                            .map((s) => s.trim())
                            .where((s) => s.isNotEmpty)
                            .toList();
                        final updated = config.copyWith(
                          repositories: repos,
                          pollInterval: _pollInterval,
                          aiPrimary: _aiPrimary,
                          aiFallback: _aiFallback,
                          retentionDays: _retentionDays,
                        );
                        try {
                          await ref.read(configNotifierProvider.notifier).save(updated);
                          if (context.mounted) showToast(context, 'Configuration saved');
                        } catch (e) {
                          if (context.mounted) showToast(context, 'Save failed: $e', isError: true);
                        }
                      },
                      child: const Text('Save'),
                    ),
                  ),
                ],
              ),
            ),
          );
        },
      ),
    );
  }

  Widget _section(String title) => Padding(
    padding: const EdgeInsets.only(bottom: 8, top: 8),
    child: Text(title, style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 15)),
  );
}
