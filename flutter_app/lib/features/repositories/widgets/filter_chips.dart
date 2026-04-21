import 'package:flutter/material.dart';

class RepoFilterChips extends StatelessWidget {
  /// Key set: 'all' | 'monitored' | 'not_monitored'.
  final Map<String, int> counts;
  final String current;
  final ValueChanged<String> onChanged;

  const RepoFilterChips({
    super.key,
    required this.counts,
    required this.current,
    required this.onChanged,
  });

  static const _labels = {
    'all': 'All',
    'monitored': 'Monitored',
    'not_monitored': 'Not monitored',
  };

  @override
  Widget build(BuildContext context) {
    final primary = Theme.of(context).colorScheme.primary;
    return Container(
      decoration: BoxDecoration(
        border: Border.all(color: Colors.grey.shade700),
        borderRadius: BorderRadius.circular(6),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          for (final e in _labels.entries) ...[
            InkWell(
              onTap: () => onChanged(e.key),
              child: Container(
                padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 7),
                color: current == e.key ? primary.withOpacity(0.22) : null,
                child: Row(children: [
                  Text(
                    e.value,
                    style: TextStyle(
                      fontSize: 12,
                      color: current == e.key ? primary : null,
                    ),
                  ),
                  const SizedBox(width: 6),
                  Container(
                    padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
                    decoration: BoxDecoration(
                      color: current == e.key
                          ? primary.withOpacity(0.32)
                          : Colors.white.withOpacity(0.06),
                      borderRadius: BorderRadius.circular(10),
                    ),
                    child: Text(
                      '${counts[e.key] ?? 0}',
                      style: TextStyle(
                        fontSize: 10,
                        color: current == e.key ? primary : Colors.grey.shade500,
                      ),
                    ),
                  ),
                ]),
              ),
            ),
            if (e.key != 'not_monitored')
              Container(width: 1, height: 28, color: Colors.grey.shade700),
          ],
        ],
      ),
    );
  }
}
