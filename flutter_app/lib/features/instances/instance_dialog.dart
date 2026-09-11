import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/api/api_client.dart';
import '../../core/api/cluster_api.dart';
import '../../core/instances/instances_providers.dart';
import '../../core/instances/models.dart';
import 'enable_hub_action.dart';

/// How an instance's API token is supplied.
enum _TokenSource { inline, env, file }

/// Add or edit a registered instance.
///
/// Pass [discovered] to prefill from a daemon found on the local network. It is
/// deliberately a separate parameter from [existing]: passing a peer as
/// `existing` would make this an edit dialog and send the save down the PATCH
/// path, when what is wanted is a registration that has simply had its address
/// filled in. The operator still types the token.
Future<void> showInstanceDialog(
  BuildContext context,
  WidgetRef ref, {
  DaemonInstance? existing,
  DiscoveredPeer? discovered,
}) {
  return showDialog<void>(
    context: context,
    builder: (context) =>
        _InstanceDialog(existing: existing, discovered: discovered),
  );
}

class _InstanceDialog extends ConsumerStatefulWidget {
  const _InstanceDialog({this.existing, this.discovered});

  final DaemonInstance? existing;
  final DiscoveredPeer? discovered;

  @override
  ConsumerState<_InstanceDialog> createState() => _InstanceDialogState();
}

class _InstanceDialogState extends ConsumerState<_InstanceDialog> {
  final _formKey = GlobalKey<FormState>();
  late final TextEditingController _name;
  late final TextEditingController _baseUrl;
  final _token = TextEditingController();
  late final TextEditingController _labels;

  _TokenSource _tokenSource = _TokenSource.inline;
  bool _skipProbe = false;
  bool _saving = false;
  String? _error;

  bool get _isEdit => widget.existing != null;

  @override
  void initState() {
    super.initState();
    final discovered = widget.discovered;
    _name = TextEditingController(
      text: widget.existing?.name ?? discovered?.displayName ?? '',
    );
    _baseUrl = TextEditingController(
      text: widget.existing?.baseUrl ?? discovered?.baseUrl ?? '',
    );
    _labels = TextEditingController(
      text: widget.existing?.labels.join(', ') ?? '',
    );
  }

  @override
  void dispose() {
    _name.dispose();
    _baseUrl.dispose();
    _token.dispose();
    _labels.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      title: Text(
        _isEdit
            ? 'Edit ${widget.existing!.displayName}'
            : widget.discovered != null
            ? 'Register ${widget.discovered!.displayName}'
            : 'Add instance',
      ),
      content: SizedBox(
        width: 460,
        child: Form(
          key: _formKey,
          child: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                TextFormField(
                  controller: _baseUrl,
                  decoration: const InputDecoration(
                    labelText: 'Base URL',
                    hintText: 'http://10.0.0.11:7842',
                    helperText:
                        'Where this hub reaches the instance. The app itself '
                        'never connects to it directly.',
                  ),
                  validator: _validateBaseUrl,
                ),
                const SizedBox(height: 12),
                TextFormField(
                  controller: _name,
                  decoration: const InputDecoration(
                    labelText: 'Name (optional)',
                    helperText:
                        'Defaults to whatever the instance calls itself.',
                  ),
                ),
                const SizedBox(height: 16),
                Text(
                  'API token',
                  style: Theme.of(context).textTheme.labelLarge,
                ),
                const SizedBox(height: 4),
                Text(
                  'Each daemon generates its own token in its data directory '
                  '(api_token). Point at an env var or a file to keep the '
                  'secret out of config.toml.',
                  style: TextStyle(
                    fontSize: 11,
                    color: Theme.of(context).colorScheme.onSurfaceVariant,
                  ),
                ),
                const SizedBox(height: 8),
                SegmentedButton<_TokenSource>(
                  segments: const [
                    ButtonSegment(
                      value: _TokenSource.inline,
                      label: Text('Value'),
                    ),
                    ButtonSegment(
                      value: _TokenSource.env,
                      label: Text('Env var'),
                    ),
                    ButtonSegment(value: _TokenSource.file, label: Text('File')),
                  ],
                  selected: {_tokenSource},
                  onSelectionChanged: (selection) =>
                      setState(() => _tokenSource = selection.first),
                ),
                const SizedBox(height: 8),
                TextFormField(
                  controller: _token,
                  obscureText: _tokenSource == _TokenSource.inline,
                  decoration: InputDecoration(
                    labelText: switch (_tokenSource) {
                      _TokenSource.inline => 'Token',
                      _TokenSource.env => 'Environment variable name',
                      _TokenSource.file => 'Absolute path to the token file',
                    },
                    hintText: switch (_tokenSource) {
                      _TokenSource.inline => '',
                      _TokenSource.env => 'HEIMDALLM_SRV_A_TOKEN',
                      _TokenSource.file => '~/.local/share/heimdallm/api_token',
                    },
                    // Editing without touching the token keeps the stored one:
                    // making the operator re-paste a secret to rename a machine
                    // would be a good way to get it wrong.
                    helperText: _isEdit ? 'Leave blank to keep the current token' : null,
                  ),
                  validator: (value) {
                    if (_isEdit && (value == null || value.trim().isEmpty)) {
                      return null;
                    }
                    if (value == null || value.trim().isEmpty) {
                      return 'Required';
                    }
                    return null;
                  },
                ),
                const SizedBox(height: 12),
                TextFormField(
                  controller: _labels,
                  decoration: const InputDecoration(
                    labelText: 'Labels (optional)',
                    hintText: 'linux, docker, eu-west',
                  ),
                ),
                if (!_isEdit) ...[
                  const SizedBox(height: 8),
                  CheckboxListTile(
                    contentPadding: EdgeInsets.zero,
                    dense: true,
                    value: _skipProbe,
                    onChanged: (v) => setState(() => _skipProbe = v ?? false),
                    title: const Text(
                      'Register without checking it answers',
                      style: TextStyle(fontSize: 13),
                    ),
                    subtitle: const Text(
                      'The hub normally verifies the instance first, so a '
                      'typo fails now rather than silently later.',
                      style: TextStyle(fontSize: 11),
                    ),
                  ),
                ],
                if (_error != null) ...[
                  const SizedBox(height: 12),
                  if (isNotAClusterHubError(_error))
                    _NotAHubError(onEnable: _enableHubMode)
                  else
                    Text(
                      _error!,
                      style: TextStyle(
                        color: Theme.of(context).colorScheme.error,
                        fontSize: 12,
                      ),
                    ),
                ],
              ],
            ),
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: _saving ? null : () => Navigator.pop(context),
          child: const Text('Cancel'),
        ),
        FilledButton(
          onPressed: _saving ? null : _save,
          child: _saving
              ? const SizedBox(
                  width: 16,
                  height: 16,
                  child: CircularProgressIndicator(strokeWidth: 2),
                )
              : Text(_isEdit ? 'Save' : 'Add'),
        ),
      ],
    );
  }

  static String? _validateBaseUrl(String? value) {
    final raw = value?.trim() ?? '';
    if (raw.isEmpty) return 'Required';
    final uri = Uri.tryParse(raw);
    if (uri == null || !uri.isAbsolute) return 'Must be an absolute URL';
    if (uri.scheme != 'http' && uri.scheme != 'https') {
      return 'Must use http or https';
    }
    if (uri.host.isEmpty) return 'Must include a host';
    if (uri.userInfo.isNotEmpty) return 'Must not embed credentials';
    return null;
  }

  Future<void> _save() async {
    if (!(_formKey.currentState?.validate() ?? false)) return;
    setState(() {
      _saving = true;
      _error = null;
    });

    final api = ref.read(hubApiClientProvider);
    final token = _token.text.trim();
    final labels = _labels.text
        .split(',')
        .map((s) => s.trim())
        .where((s) => s.isNotEmpty)
        .toList();

    try {
      if (_isEdit) {
        await api.patchInstance(
          widget.existing!.id,
          name: _name.text.trim(),
          baseUrl: _baseUrl.text.trim(),
          labels: labels,
          token: _tokenSource == _TokenSource.inline && token.isNotEmpty
              ? token
              : null,
          tokenEnv: _tokenSource == _TokenSource.env && token.isNotEmpty
              ? token
              : null,
          tokenFile: _tokenSource == _TokenSource.file && token.isNotEmpty
              ? token
              : null,
        );
      } else {
        await api.registerInstance(
          baseUrl: _baseUrl.text.trim(),
          name: _name.text.trim(),
          labels: labels,
          skipProbe: _skipProbe,
          // Pin the identity seen when the peer was discovered. Between the
          // browse that proposed it and this click, something else on the LAN
          // could have taken the name; the daemon refuses the registration if
          // the machine at this address is not the one that was found.
          expectInstanceId: widget.discovered?.instanceId,
          token: _tokenSource == _TokenSource.inline ? token : null,
          tokenEnv: _tokenSource == _TokenSource.env ? token : null,
          tokenFile: _tokenSource == _TokenSource.file ? token : null,
        );
      }
      ref.invalidate(daemonInstancesProvider);
      ref.invalidate(routingRulesProvider);
      if (mounted) Navigator.pop(context);
    } on ApiException catch (e) {
      if (mounted) {
        setState(() {
          _error = e.message;
          _saving = false;
        });
      }
    } catch (e) {
      if (mounted) {
        setState(() {
          _error = '$e';
          _saving = false;
        });
      }
    }
  }

  /// The dialog's own "not a hub" recovery action. Closes on success: the
  /// restart tears down the daemon connection this form is talking to, so a
  /// dialog holding stale form state over a restarting daemon is worse than
  /// a clean re-open — the honest instruction is "enable it, then add the
  /// instance again", not a promise to resume automatically.
  Future<void> _enableHubMode() async {
    final ok = await enableHubMode(context, ref);
    if (ok && mounted) Navigator.pop(context);
  }
}

/// Humanises the daemon's raw `hubOnly` refusal into an explanation plus a
/// one-click fix, instead of showing the bare sentinel string.
class _NotAHubError extends StatelessWidget {
  const _NotAHubError({required this.onEnable});

  final VoidCallback onEnable;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.all(10),
      decoration: BoxDecoration(
        color: Theme.of(
          context,
        ).colorScheme.errorContainer.withValues(alpha: 0.4),
        borderRadius: BorderRadius.circular(6),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(
                Icons.hub_outlined,
                size: 16,
                color: Theme.of(context).colorScheme.onErrorContainer,
              ),
              const SizedBox(width: 6),
              Expanded(
                child: Text(
                  'This daemon is not a cluster hub yet',
                  style: TextStyle(
                    fontSize: 12,
                    fontWeight: FontWeight.w600,
                    color: Theme.of(context).colorScheme.onErrorContainer,
                  ),
                ),
              ),
            ],
          ),
          const SizedBox(height: 4),
          Text(
            'Only a hub can register other instances. Enabling hub mode '
            'saves the setting and restarts the daemon; then add this '
            'instance again.',
            style: TextStyle(
              fontSize: 11,
              color: Theme.of(context).colorScheme.onErrorContainer,
            ),
          ),
          const SizedBox(height: 8),
          FilledButton.icon(
            onPressed: onEnable,
            icon: const Icon(Icons.hub, size: 16),
            label: const Text('Enable hub mode'),
          ),
        ],
      ),
    );
  }
}
