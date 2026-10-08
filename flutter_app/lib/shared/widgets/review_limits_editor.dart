import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/models/config_model.dart';

/// Three numeric fields (per minute / hour / day) for a review budget.
/// 0 or empty means "no limit" for that window.
class ReviewLimitsFields extends StatefulWidget {
  final ReviewLimits value;
  final ValueChanged<ReviewLimits> onChanged;

  /// Prefix for the field keys, so tests and screens with several editors can
  /// address each one (e.g. `global`, `org`, `agent-claude`).
  final String keyPrefix;

  const ReviewLimitsFields({
    super.key,
    required this.value,
    required this.onChanged,
    required this.keyPrefix,
  });

  @override
  State<ReviewLimitsFields> createState() => _ReviewLimitsFieldsState();
}

class _ReviewLimitsFieldsState extends State<ReviewLimitsFields> {
  late final TextEditingController _minute;
  late final TextEditingController _hour;
  late final TextEditingController _day;

  static String _text(int v) => v > 0 ? '$v' : '';

  @override
  void initState() {
    super.initState();
    _minute = TextEditingController(text: _text(widget.value.perMinute));
    _hour = TextEditingController(text: _text(widget.value.perHour));
    _day = TextEditingController(text: _text(widget.value.perDay));
  }

  @override
  void didUpdateWidget(ReviewLimitsFields oldWidget) {
    super.didUpdateWidget(oldWidget);
    // Follow external resets (e.g. a server refresh) without clobbering what
    // the operator is typing: only rewrite a field whose value changed.
    void sync(TextEditingController c, int v) {
      if ((int.tryParse(c.text) ?? 0) != v) c.text = _text(v);
    }

    sync(_minute, widget.value.perMinute);
    sync(_hour, widget.value.perHour);
    sync(_day, widget.value.perDay);
  }

  @override
  void dispose() {
    _minute.dispose();
    _hour.dispose();
    _day.dispose();
    super.dispose();
  }

  int _parse(String v) {
    final n = int.tryParse(v) ?? 0;
    return n.clamp(0, ReviewLimits.maxValue);
  }

  Widget _field(
    String window,
    TextEditingController controller,
    ReviewLimits Function(int) apply,
  ) {
    return Expanded(
      child: TextFormField(
        key: ValueKey('${widget.keyPrefix}-review-limit-$window'),
        controller: controller,
        decoration: InputDecoration(
          labelText: 'Per $window',
          hintText: 'No limit',
          border: const OutlineInputBorder(),
          isDense: true,
        ),
        keyboardType: TextInputType.number,
        inputFormatters: [FilteringTextInputFormatter.digitsOnly],
        onChanged: (v) => widget.onChanged(apply(_parse(v))),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    return Row(
      children: [
        _field('minute', _minute, (n) => widget.value.copyWith(perMinute: n)),
        const SizedBox(width: 8),
        _field('hour', _hour, (n) => widget.value.copyWith(perHour: n)),
        const SizedBox(width: 8),
        _field('day', _day, (n) => widget.value.copyWith(perDay: n)),
      ],
    );
  }
}

/// Org/repo review budget editor. A scope either has its own budget or none;
/// the global budget (and the org's, for a repo) always applies on top, so
/// "none" is not "inherit" but "no extra limit here".
class ReviewLimitsOverrideEditor extends StatelessWidget {
  final ReviewLimits? value;
  final String scopeLabel;
  final String keyPrefix;
  final ValueChanged<ReviewLimits> onChanged;
  final VoidCallback onRemove;

  const ReviewLimitsOverrideEditor({
    super.key,
    required this.value,
    required this.scopeLabel,
    required this.keyPrefix,
    required this.onChanged,
    required this.onRemove,
  });

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final helper = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final current = value;
    if (current == null) {
      return Row(
        children: [
          Expanded(
            child: Text(
              'No $scopeLabel limit. The global review limits still apply.',
              style: helper,
            ),
          ),
          TextButton.icon(
            key: ValueKey('$keyPrefix-review-limits-add'),
            onPressed: () => onChanged(const ReviewLimits(perHour: 10)),
            icon: const Icon(Icons.add, size: 16),
            label: const Text('Add limit'),
          ),
        ],
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        ReviewLimitsFields(
          value: current,
          keyPrefix: keyPrefix,
          onChanged: onChanged,
        ),
        const SizedBox(height: 6),
        Row(
          children: [
            Expanded(
              child: Text(
                'Counts only this $scopeLabel\'s reviews, on top of the '
                'global limits. Reviews over the limit wait for the next free slot.',
                style: helper,
              ),
            ),
            TextButton(
              key: ValueKey('$keyPrefix-review-limits-remove'),
              onPressed: onRemove,
              child: const Text('Remove'),
            ),
          ],
        ),
      ],
    );
  }
}
