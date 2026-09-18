import 'package:flutter/material.dart';
import 'package:mix/mix.dart';

class TypeBadge extends StatelessWidget {
  final String type; // 'pr', 'it', 'dev'
  const TypeBadge({super.key, required this.type});

  @override
  Widget build(BuildContext context) {
    final (label, color) = switch (type) {
      'pr' => ('PR', Colors.blue),
      'it' => ('IT', Colors.orange),
      'dev' => ('DEV', Colors.green),
      _ => ('?', Colors.grey),
    };
    return Box(
      style: BoxStyler()
          .width(32)
          .height(32)
          .alignment(Alignment.center)
          .color(color.withValues(alpha: 0.2))
          .borderRounded(6)
          .borderAll(color: color.withValues(alpha: 0.5)),
      child: StyledText(
        label,
        style: TextStyler()
            .fontSize(10)
            .fontWeight(FontWeight.w700)
            .color(color),
      ),
    );
  }
}
