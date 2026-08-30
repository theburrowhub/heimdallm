import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/api/api_client.dart';
import '../dashboard/dashboard_providers.dart';
import 'merge_tracking_providers.dart';

/// Opens the Merge tab's own add-a-PR dialog.
Future<void> showAddMergePRDialog(BuildContext context) async {
  final added = await showDialog<bool>(
    context: context,
    builder: (_) => const AddMergePRDialog(),
  );
  if (added != true || !context.mounted) return;

  ScaffoldMessenger.of(context).showSnackBar(
    const SnackBar(
      content: Text('PR tracked — its merge state appears on the next check.'),
    ),
  );
}

/// Paste-a-link dialog for merge tracking.
///
/// Separate from the Activity tab's Add PR, which routes through the review
/// pipeline: that pipeline refuses a PR the authenticated account authored, and
/// Heimdallm authenticates as the operator, so pasting your own PR there is
/// answered with a `self_authored` skip and nothing else. These are precisely
/// the PRs merge tracking is for, so they get their own door.
class AddMergePRDialog extends ConsumerStatefulWidget {
  const AddMergePRDialog({super.key});

  @override
  ConsumerState<AddMergePRDialog> createState() => _AddMergePRDialogState();
}

class _AddMergePRDialogState extends ConsumerState<AddMergePRDialog> {
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
      await ref.read(apiClientProvider).addMergeTracking(url);
      if (!mounted) return;
      ref.read(mergeTrackingRefreshProvider.notifier).update((s) => s + 1);
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
      title: const Text('Track a pull request'),
      content: SizedBox(
        width: 460,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text(
              'Paste a GitHub PR link. Its repository is added to the monitored '
              'list and the PR is tracked for merge readiness. No review is '
              'triggered — this is the path for your own pull requests.',
            ),
            const SizedBox(height: 14),
            TextField(
              key: const Key('add-merge-pr-url-field'),
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
              : const Text('Track'),
        ),
      ],
    );
  }
}
