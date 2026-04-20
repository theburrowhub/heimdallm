import 'package:flutter/material.dart';

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
    return Container(
      width: 32,
      height: 32,
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.2),
        borderRadius: BorderRadius.circular(6),
        border: Border.all(color: color.withValues(alpha: 0.5)),
      ),
      child: Text(label,
          style: TextStyle(
              fontSize: 10, fontWeight: FontWeight.w700, color: color)),
    );
  }
}
