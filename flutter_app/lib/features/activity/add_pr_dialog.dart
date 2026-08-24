import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/api/api_client.dart';
import '../dashboard/dashboard_providers.dart';
import 'activity_providers.dart';

/// Opens the shared dialog for manually adding and immediately reviewing a PR.
Future<void> showAddPRDialog(BuildContext context) async {
  final added = await showDialog<bool>(
    context: context,
    builder: (_) => const AddPRDialog(),
  );
  if (added != true || !context.mounted) return;

  ScaffoldMessenger.of(context).showSnackBar(
    const SnackBar(
      content: Text('PR added — repository monitored and review started.'),
    ),
  );
}

/// Paste-a-link dialog shared by the main Activity list and Activity log.
class AddPRDialog extends ConsumerStatefulWidget {
  const AddPRDialog({super.key});

  @override
  ConsumerState<AddPRDialog> createState() => _AddPRDialogState();
}

class _AddPRDialogState extends ConsumerState<AddPRDialog> {
  final _controller = TextEditingController();
  bool _submitting = false;
  String? _error;

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  static bool _looksLikePRUrl(String value) {
    final url = value.trim();
    return url.contains('github.com/') && url.contains('/pull/');
  }

  Future<void> _submit() async {
    final url = _controller.text.trim();
    if (!_looksLikePRUrl(url)) {
      setState(
        () => _error =
            'Enter a GitHub PR link, e.g. https://github.com/owner/repo/pull/123',
      );
      return;
    }

    setState(() {
      _submitting = true;
      _error = null;
    });

    try {
      await ref.read(apiClientProvider).addPRByUrl(url);
      if (!mounted) return;

      // The new PR must appear in the primary list immediately. Refresh the
      // activity log as well because adding a PR starts its review.
      ref.invalidate(prsProvider);
      ref.invalidate(activityEntriesProvider);
      ref.invalidate(activityOptionsProvider);
      Navigator.of(context).pop(true);
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _submitting = false;
        _error = error is ApiException ? error.message : error.toString();
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      title: const Text('Add a pull request'),
      content: SizedBox(
        width: 460,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text(
              'Paste a GitHub PR link. Its repository is added to the '
              'monitored list and the PR is reviewed right away.',
            ),
            const SizedBox(height: 14),
            TextField(
              key: const Key('add-pr-url-field'),
              controller: _controller,
              autofocus: true,
              enabled: !_submitting,
              keyboardType: TextInputType.url,
              decoration: const InputDecoration(
                labelText: 'GitHub PR URL',
                hintText: 'https://github.com/owner/repo/pull/123',
                border: OutlineInputBorder(),
              ),
              onSubmitted: (_) {
                if (!_submitting) _submit();
              },
            ),
            if (_error != null) ...[
              const SizedBox(height: 10),
              Text(
                _error!,
                style: TextStyle(color: Theme.of(context).colorScheme.error),
              ),
            ],
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: _submitting ? null : () => Navigator.of(context).pop(),
          child: const Text('Cancel'),
        ),
        FilledButton(
          onPressed: _submitting ? null : _submit,
          child: _submitting
              ? const SizedBox(
                  width: 16,
                  height: 16,
                  child: CircularProgressIndicator(strokeWidth: 2),
                )
              : const Text('Add & review'),
        ),
      ],
    );
  }
}
