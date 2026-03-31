import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../dashboard/dashboard_providers.dart';

class StatsScreen extends ConsumerWidget {
  const StatsScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final statsAsync = ref.watch(statsProvider);

    return statsAsync.when(
      loading: () => const Center(child: CircularProgressIndicator()),
      error: (e, _) => Center(child: Text('Error loading stats: $e')),
      data: (stats) => _StatsBody(stats: stats),
    );
  }
}

class _StatsBody extends StatelessWidget {
  final Map<String, dynamic> stats;
  const _StatsBody({required this.stats});

  @override
  Widget build(BuildContext context) {
    final totalReviews = stats['total_reviews'] as int? ?? 0;
    final bySeverity = (stats['by_severity'] as Map<String, dynamic>?) ?? {};
    final byCLI = (stats['by_cli'] as Map<String, dynamic>?) ?? {};
    final topRepos = (stats['top_repos'] as List<dynamic>?) ?? [];
    final last7 = (stats['reviews_last_7_days'] as List<dynamic>?) ?? [];
    final avgIssues = (stats['avg_issues_per_review'] as num?)?.toDouble() ?? 0;

    return SingleChildScrollView(
      padding: const EdgeInsets.all(20),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          // Summary row
          Row(
            children: [
              _StatCard(
                icon: Icons.rate_review,
                label: 'Total Reviews',
                value: '$totalReviews',
                color: Colors.blue,
              ),
              const SizedBox(width: 12),
              _StatCard(
                icon: Icons.bug_report,
                label: 'Avg Issues / Review',
                value: avgIssues.toStringAsFixed(1),
                color: Colors.orange,
              ),
              const SizedBox(width: 12),
              _StatCard(
                icon: Icons.warning_amber,
                label: 'High Severity',
                value: '${bySeverity['high'] ?? 0}',
                color: Colors.red.shade700,
              ),
              const SizedBox(width: 12),
              _StatCard(
                icon: Icons.check_circle,
                label: 'Low Severity',
                value: '${bySeverity['low'] ?? 0}',
                color: Colors.green.shade700,
              ),
            ],
          ),

          const SizedBox(height: 24),

          // Reviews last 7 days
          if (last7.isNotEmpty) ...[
            _sectionTitle(context, 'Reviews Last 7 Days'),
            const SizedBox(height: 8),
            _BarChart(days: last7),
            const SizedBox(height: 24),
          ],

          // Severity distribution
          if (bySeverity.isNotEmpty) ...[
            _sectionTitle(context, 'By Severity'),
            const SizedBox(height: 8),
            _PillRow(data: bySeverity, colorMap: {
              'high': Colors.red.shade700,
              'medium': Colors.orange.shade700,
              'low': Colors.green.shade700,
            }),
            const SizedBox(height: 24),
          ],

          // By AI agent
          if (byCLI.isNotEmpty) ...[
            _sectionTitle(context, 'By AI Agent'),
            const SizedBox(height: 8),
            _PillRow(data: byCLI, colorMap: const {
              'claude': Color(0xFF7C4DFF),
              'gemini': Color(0xFF1565C0),
              'codex':  Color(0xFF00695C),
            }),
            const SizedBox(height: 24),
          ],

          // Top repos
          if (topRepos.isNotEmpty) ...[
            _sectionTitle(context, 'Top Repos by Reviews'),
            const SizedBox(height: 8),
            ...topRepos.map((r) {
              final repo = r['repo'] as String? ?? '';
              final count = r['count'] as int? ?? 0;
              final max = (topRepos.first['count'] as int? ?? 1);
              return Padding(
                padding: const EdgeInsets.only(bottom: 6),
                child: Row(
                  children: [
                    SizedBox(
                      width: 240,
                      child: Text(repo,
                          style: const TextStyle(fontSize: 13),
                          overflow: TextOverflow.ellipsis),
                    ),
                    const SizedBox(width: 8),
                    Expanded(
                      child: ClipRRect(
                        borderRadius: BorderRadius.circular(4),
                        child: LinearProgressIndicator(
                          value: count / max,
                          backgroundColor: Colors.grey.shade800,
                          minHeight: 10,
                        ),
                      ),
                    ),
                    const SizedBox(width: 8),
                    Text('$count', style: const TextStyle(fontSize: 12, color: Colors.grey)),
                  ],
                ),
              );
            }),
          ],
        ],
      ),
    );
  }

  Widget _sectionTitle(BuildContext context, String title) {
    return Text(title,
        style: Theme.of(context)
            .textTheme
            .titleSmall
            ?.copyWith(fontWeight: FontWeight.bold));
  }
}

class _StatCard extends StatelessWidget {
  final IconData icon;
  final String label;
  final String value;
  final Color color;

  const _StatCard({
    required this.icon,
    required this.label,
    required this.value,
    required this.color,
  });

  @override
  Widget build(BuildContext context) {
    return Expanded(
      child: Card(
        child: Padding(
          padding: const EdgeInsets.all(16),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Icon(icon, color: color, size: 22),
              const SizedBox(height: 8),
              Text(value,
                  style: TextStyle(
                      fontSize: 26,
                      fontWeight: FontWeight.bold,
                      color: color)),
              const SizedBox(height: 4),
              Text(label,
                  style: const TextStyle(fontSize: 12, color: Colors.grey)),
            ],
          ),
        ),
      ),
    );
  }
}

class _PillRow extends StatelessWidget {
  final Map<String, dynamic> data;
  final Map<String, Color> colorMap;
  const _PillRow({required this.data, required this.colorMap});

  @override
  Widget build(BuildContext context) {
    return Wrap(
      spacing: 8,
      runSpacing: 8,
      children: data.entries.map((e) {
        final color = colorMap[e.key] ?? Colors.grey;
        return Container(
          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
          decoration: BoxDecoration(
            color: color.withValues(alpha: 0.15),
            border: Border.all(color: color.withValues(alpha: 0.5)),
            borderRadius: BorderRadius.circular(20),
          ),
          child: Text(
            '${e.key}  ${e.value}',
            style: TextStyle(color: color, fontWeight: FontWeight.w600, fontSize: 13),
          ),
        );
      }).toList(),
    );
  }
}

class _BarChart extends StatelessWidget {
  final List<dynamic> days;
  const _BarChart({required this.days});

  @override
  Widget build(BuildContext context) {
    final maxCount = days.map((d) => (d['count'] as int? ?? 0)).fold(1, (a, b) => a > b ? a : b);

    return SizedBox(
      height: 96,
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.end,
        children: days.map((d) {
          final count = d['count'] as int? ?? 0;
          final day = (d['day'] as String? ?? '').substring(5); // MM-DD
          return Expanded(
            child: Padding(
              padding: const EdgeInsets.symmetric(horizontal: 3),
              child: Column(
                mainAxisAlignment: MainAxisAlignment.end,
                children: [
                  Text('$count', style: const TextStyle(fontSize: 10, color: Colors.grey)),
                  const SizedBox(height: 2),
                  ClipRRect(
                    borderRadius: BorderRadius.circular(3),
                    child: Container(
                      height: (count / maxCount) * 50,
                      color: Theme.of(context).colorScheme.primary,
                    ),
                  ),
                  const SizedBox(height: 4),
                  Text(day, style: const TextStyle(fontSize: 10, color: Colors.grey)),
                ],
              ),
            ),
          );
        }).toList(),
      ),
    );
  }
}
