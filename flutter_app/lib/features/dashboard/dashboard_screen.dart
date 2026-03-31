import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../../core/models/pr.dart';
import '../../shared/widgets/severity_badge.dart';
import 'dashboard_providers.dart';

class DashboardScreen extends ConsumerWidget {
  const DashboardScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final prsAsync = ref.watch(prsProvider);

    return Scaffold(
      appBar: AppBar(
        title: const Text('Heimdallr'),
        actions: [
          IconButton(
            icon: const Icon(Icons.settings),
            onPressed: () => context.go('/config'),
          ),
          IconButton(
            icon: const Icon(Icons.refresh),
            onPressed: () => ref.invalidate(prsProvider),
          ),
        ],
      ),
      body: prsAsync.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (e, _) => Center(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const Icon(Icons.error_outline, size: 48, color: Colors.red),
              const SizedBox(height: 8),
              Text('Failed to load PRs: $e'),
              TextButton(
                onPressed: () => ref.invalidate(prsProvider),
                child: const Text('Retry'),
              ),
            ],
          ),
        ),
        data: (prs) => prs.isEmpty
            ? const Center(child: Text('No open PRs found'))
            : _PRTable(prs: prs),
      ),
    );
  }
}

class _PRTable extends ConsumerWidget {
  final List<PR> prs;
  const _PRTable({required this.prs});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    return SingleChildScrollView(
      child: DataTable(
        columns: const [
          DataColumn(label: Text('Repo')),
          DataColumn(label: Text('PR')),
          DataColumn(label: Text('Author')),
          DataColumn(label: Text('Severity')),
          DataColumn(label: Text('Status')),
          DataColumn(label: Text('Actions')),
        ],
        rows: prs.map((pr) => DataRow(
          cells: [
            DataCell(Text(pr.repo)),
            DataCell(
              TextButton(
                onPressed: () => context.go('/prs/${pr.id}'),
                child: Text(pr.title),
              ),
            ),
            DataCell(Text(pr.author)),
            DataCell(pr.latestReview != null
                ? SeverityBadge(severity: pr.latestReview!.severity)
                : const Text('—')),
            DataCell(Text(pr.latestReview != null ? 'Reviewed' : 'Pending')),
            DataCell(
              ElevatedButton(
                onPressed: () async {
                  final api = ref.read(apiClientProvider);
                  await api.triggerReview(pr.id);
                  ref.invalidate(prsProvider);
                },
                child: const Text('Review Now'),
              ),
            ),
          ],
        )).toList(),
      ),
    );
  }
}
