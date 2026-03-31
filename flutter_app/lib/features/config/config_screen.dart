import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/daemon/daemon_lifecycle.dart';
import '../../core/models/config_model.dart';
import '../../core/setup/first_run_setup.dart';
import '../../shared/widgets/toast.dart';
import 'config_providers.dart';

class ConfigScreen extends ConsumerStatefulWidget {
  const ConfigScreen({super.key});

  @override
  ConsumerState<ConfigScreen> createState() => _ConfigScreenState();
}

class _ConfigScreenState extends ConsumerState<ConfigScreen> {
  final _reposController = TextEditingController();
  final _tokenController = TextEditingController();
  String _pollInterval = '5m';
  String _aiPrimary = 'claude';
  String _aiFallback = '';
  int _retentionDays = 90;
  bool _initialized = false;
  bool _obscureToken = true;

  @override
  void dispose() {
    _reposController.dispose();
    _tokenController.dispose();
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
    // Pre-fill token field from Keychain if available
    FirstRunSetup.getToken().then((t) {
      if (t != null && mounted) {
        setState(() => _tokenController.text = t);
      }
    });
  }

  @override
  Widget build(BuildContext context) {
    final configAsync = ref.watch(configNotifierProvider);
    final daemonAsync = ref.watch(daemonHealthProvider);

    final daemonRunning = daemonAsync.valueOrNull ?? false;

    return Scaffold(
      appBar: AppBar(title: const Text('Configuración')),
      body: configAsync.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (e, _) => _setupRequired(context, daemonRunning),
        data: (config) {
          _initFrom(config);
          return _form(context, config, daemonRunning);
        },
      ),
    );
  }

  Widget _setupRequired(BuildContext context, bool daemonRunning) {
    return _form(context, const AppConfig(), daemonRunning);
  }

  Widget _form(BuildContext context, AppConfig config, bool daemonRunning) {
    return SingleChildScrollView(
      padding: const EdgeInsets.all(24),
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 600),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            // Setup banner when daemon is not running
            if (!daemonRunning) ...[
              Container(
                width: double.infinity,
                padding: const EdgeInsets.all(12),
                decoration: BoxDecoration(
                  color: Colors.orange.shade700.withValues(alpha: 0.15),
                  border: Border.all(color: Colors.orange.shade700),
                  borderRadius: BorderRadius.circular(8),
                ),
                child: Row(
                  children: [
                    Icon(Icons.info_outline, color: Colors.orange.shade700),
                    const SizedBox(width: 8),
                    const Expanded(
                      child: Text(
                        'El daemon no está corriendo. Completa la configuración y pulsa "Guardar e iniciar" para arrancarlo.',
                      ),
                    ),
                  ],
                ),
              ),
              const SizedBox(height: 20),
            ],

            // Token section (always visible)
            _section('Token de GitHub'),
            TextFormField(
              controller: _tokenController,
              obscureText: _obscureToken,
              decoration: InputDecoration(
                labelText: 'Personal Access Token',
                hintText: 'ghp_...',
                border: const OutlineInputBorder(),
                helperText: 'Permisos necesarios: repo, read:org',
                suffixIcon: IconButton(
                  icon: Icon(_obscureToken ? Icons.visibility : Icons.visibility_off),
                  onPressed: () => setState(() => _obscureToken = !_obscureToken),
                ),
              ),
            ),
            const SizedBox(height: 16),

            // Repos section
            _section('Repositorios'),
            TextFormField(
              controller: _reposController,
              decoration: const InputDecoration(
                labelText: 'Repositorios (separados por coma)',
                hintText: 'org/repo1, org/repo2',
                border: OutlineInputBorder(),
              ),
            ),
            const SizedBox(height: 16),

            _section('Polling'),
            DropdownButtonFormField<String>(
              value: _pollInterval,
              decoration: const InputDecoration(
                labelText: 'Intervalo de sondeo',
                border: OutlineInputBorder(),
              ),
              items: ['1m', '5m', '30m', '1h']
                  .map((v) => DropdownMenuItem(value: v, child: Text(v)))
                  .toList(),
              onChanged: (v) => setState(() => _pollInterval = v!),
            ),
            const SizedBox(height: 16),

            _section('Modelo de IA'),
            DropdownButtonFormField<String>(
              value: _aiPrimary,
              decoration: const InputDecoration(
                labelText: 'Agente principal',
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
                labelText: 'Agente fallback (opcional)',
                border: OutlineInputBorder(),
              ),
              items: [
                const DropdownMenuItem<String>(value: null, child: Text('Ninguno')),
                ...['claude', 'gemini', 'codex']
                    .map((v) => DropdownMenuItem<String>(value: v, child: Text(v))),
              ],
              onChanged: (v) => setState(() => _aiFallback = v ?? ''),
            ),
            const SizedBox(height: 16),

            _section('Retención'),
            TextFormField(
              initialValue: _retentionDays.toString(),
              decoration: const InputDecoration(
                labelText: 'Guardar reviews durante (días, 0 = siempre)',
                border: OutlineInputBorder(),
              ),
              keyboardType: TextInputType.number,
              onChanged: (v) => _retentionDays = int.tryParse(v) ?? 90,
            ),
            const SizedBox(height: 24),

            // Save button — different behavior depending on daemon state
            SizedBox(
              width: double.infinity,
              child: daemonRunning
                  ? _saveButton(context, config)
                  : _setupButton(context, config),
            ),
          ],
        ),
      ),
    );
  }

  Widget _saveButton(BuildContext context, AppConfig config) {
    return ElevatedButton(
      onPressed: () async {
        final repos = _buildRepos();
        final updated = _buildConfig(config, repos);
        try {
          // Also update token in Keychain if user changed it
          final token = _tokenController.text.trim();
          if (token.isNotEmpty) {
            await FirstRunSetup.storeToken(token);
          }
          await ref.read(configNotifierProvider.notifier).save(updated);
          if (context.mounted) showToast(context, 'Configuración guardada');
        } catch (e) {
          if (context.mounted) showToast(context, 'Error: $e', isError: true);
        }
      },
      child: const Text('Guardar'),
    );
  }

  Widget _setupButton(BuildContext context, AppConfig config) {
    final isLoading = ref.watch(configNotifierProvider).isLoading;
    return FilledButton.icon(
      icon: isLoading
          ? const SizedBox(
              width: 16, height: 16,
              child: CircularProgressIndicator(strokeWidth: 2, color: Colors.white),
            )
          : const Icon(Icons.rocket_launch),
      label: Text(isLoading ? 'Iniciando...' : 'Guardar e iniciar Heimdallr'),
      onPressed: isLoading ? null : () async {
        final token = _tokenController.text.trim();
        if (token.isEmpty) {
          showToast(context, 'El token de GitHub es obligatorio', isError: true);
          return;
        }
        final repos = _buildRepos();
        if (repos.isEmpty) {
          showToast(context, 'Añade al menos un repositorio', isError: true);
          return;
        }
        final updated = _buildConfig(config, repos);
        await ref.read(configNotifierProvider.notifier).saveAndStartDaemon(
          token: token,
          config: updated,
          daemonBinaryPath: DaemonLifecycle.defaultBinaryPath(),
        );
        if (context.mounted) {
          final state = ref.read(configNotifierProvider);
          if (state.hasError) {
            showToast(context, 'Error: ${state.error}', isError: true);
          } else {
            showToast(context, '¡Heimdallr iniciado correctamente!');
            ref.invalidate(daemonHealthProvider);
          }
        }
      },
    );
  }

  List<String> _buildRepos() => _reposController.text
      .split(',')
      .map((s) => s.trim())
      .where((s) => s.isNotEmpty)
      .toList();

  AppConfig _buildConfig(AppConfig base, List<String> repos) => base.copyWith(
    repositories: repos,
    pollInterval: _pollInterval,
    aiPrimary: _aiPrimary,
    aiFallback: _aiFallback,
    retentionDays: _retentionDays,
  );

  Widget _section(String title) => Padding(
    padding: const EdgeInsets.only(bottom: 8, top: 8),
    child: Text(title, style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 15)),
  );
}
