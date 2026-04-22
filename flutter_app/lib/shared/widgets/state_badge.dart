import 'package:flutter/material.dart';

class StateBadge extends StatelessWidget {
  final String state;
  const StateBadge({super.key, required this.state});

  bool get _isOpen => state == 'open';

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
      decoration: BoxDecoration(
        color: _isOpen ? Colors.green.shade700 : Colors.grey.shade600,
        borderRadius: BorderRadius.circular(10),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(
            _isOpen ? Icons.circle_outlined : Icons.check_circle,
            size: 12,
            color: Colors.white,
          ),
          const SizedBox(width: 4),
          Text(
            _isOpen ? 'Open' : 'Closed',
            style: const TextStyle(color: Colors.white, fontSize: 10, fontWeight: FontWeight.w600),
          ),
        ],
      ),
    );
  }
}
