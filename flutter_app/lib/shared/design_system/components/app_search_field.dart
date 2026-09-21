import 'package:flutter/material.dart';

import '../tokens.dart';

/// A themed, compact search field shared by every filter toolbar.
///
/// Owns its [TextEditingController]/[FocusNode] so the caller only holds the
/// current [value] as plain state; this widget resyncs the controller when
/// [value] changes externally (e.g. a "Reset" button) without clobbering the
/// cursor position while the field has focus while typing — the fix that was
/// previously duplicated (and easy to get wrong) across three separate
/// search fields (`activity_filter_bar.dart`, `events_tab.dart`,
/// `repos_screen.dart`).
class AppSearchField extends StatefulWidget {
  final String value;
  final ValueChanged<String> onChanged;
  final String hintText;
  final double width;

  const AppSearchField({
    super.key,
    required this.value,
    required this.onChanged,
    this.hintText = 'Search…',
    this.width = 200,
  });

  @override
  State<AppSearchField> createState() => _AppSearchFieldState();
}

class _AppSearchFieldState extends State<AppSearchField> {
  late final TextEditingController _controller = TextEditingController(
    text: widget.value,
  );
  final _focusNode = FocusNode();

  @override
  void dispose() {
    _controller.dispose();
    _focusNode.dispose();
    super.dispose();
  }

  @override
  void didUpdateWidget(covariant AppSearchField oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (widget.value != _controller.text && !_focusNode.hasFocus) {
      _controller.text = widget.value;
    }
  }

  @override
  Widget build(BuildContext context) {
    final borderColor = AppColors.border.resolve(context);
    final focusColor = AppColors.focus.resolve(context);
    final pillRadius = BorderRadius.all(AppRadius.pill.resolve(context));

    return SizedBox(
      width: widget.width,
      child: TextField(
        controller: _controller,
        focusNode: _focusNode,
        style: const TextStyle(fontSize: 12),
        decoration: InputDecoration(
          hintText: widget.hintText,
          hintStyle: const TextStyle(fontSize: 12),
          prefixIcon: const Icon(Icons.search, size: 16),
          prefixIconConstraints: const BoxConstraints(
            minWidth: 32,
            minHeight: 0,
          ),
          isDense: true,
          contentPadding: const EdgeInsets.symmetric(
            horizontal: 8,
            vertical: 8,
          ),
          border: OutlineInputBorder(
            borderRadius: pillRadius,
            borderSide: BorderSide(color: borderColor),
          ),
          enabledBorder: OutlineInputBorder(
            borderRadius: pillRadius,
            borderSide: BorderSide(color: borderColor),
          ),
          focusedBorder: OutlineInputBorder(
            borderRadius: pillRadius,
            borderSide: BorderSide(color: focusColor, width: 1.5),
          ),
        ),
        onChanged: widget.onChanged,
      ),
    );
  }
}
