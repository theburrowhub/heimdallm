import 'dart:async';
import 'package:flutter/material.dart';

/// Shows a temporary toast notification that auto-dismisses.
/// Uses Overlay instead of SnackBar to avoid macOS desktop SnackBar
/// persistence bugs.
///
/// Optional [actionLabel] and [onAction] add a tappable button (e.g. "Undo")
/// that fires [onAction] and immediately dismisses the toast.
void showToast(
  BuildContext context,
  String message, {
  bool isError = false,
  Duration duration = const Duration(seconds: 3),
  String? actionLabel,
  VoidCallback? onAction,
}) {
  final overlay = Overlay.of(context, rootOverlay: true);
  late OverlayEntry entry;
  entry = OverlayEntry(
    builder: (ctx) => _ToastWidget(
      message: message,
      isError: isError,
      duration: duration,
      actionLabel: actionLabel,
      onAction: onAction,
      onDismiss: () {
        try { entry.remove(); } catch (_) {}
      },
    ),
  );
  overlay.insert(entry);
}

class _ToastWidget extends StatefulWidget {
  final String message;
  final bool isError;
  final Duration duration;
  final String? actionLabel;
  final VoidCallback? onAction;
  final VoidCallback onDismiss;

  const _ToastWidget({
    required this.message,
    required this.isError,
    required this.duration,
    this.actionLabel,
    this.onAction,
    required this.onDismiss,
  });

  @override
  State<_ToastWidget> createState() => _ToastWidgetState();
}

class _ToastWidgetState extends State<_ToastWidget>
    with SingleTickerProviderStateMixin {
  late final AnimationController _anim;
  Timer? _dismissTimer;

  @override
  void initState() {
    super.initState();
    _anim = AnimationController(
      vsync: this,
      duration: const Duration(milliseconds: 200),
    )..forward();
    _dismissTimer = Timer(widget.duration, _fadeOut);
  }

  void _fadeOut() {
    if (!mounted) return;
    _anim.reverse().then((_) {
      if (mounted) widget.onDismiss();
    });
  }

  void _onActionTap() {
    _dismissTimer?.cancel();
    widget.onAction?.call();
    _fadeOut();
  }

  @override
  void dispose() {
    _dismissTimer?.cancel();
    _anim.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Positioned(
      bottom: 24,
      left: 24,
      right: 24,
      child: FadeTransition(
        opacity: _anim,
        child: Align(
          alignment: Alignment.bottomCenter,
          child: Material(
            color: Colors.transparent,
            child: Container(
              constraints: const BoxConstraints(maxWidth: 500),
              padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
              decoration: BoxDecoration(
                color: widget.isError ? Colors.red.shade700 : Colors.green.shade700,
                borderRadius: BorderRadius.circular(8),
                boxShadow: [
                  BoxShadow(
                    color: Colors.black.withValues(alpha: 0.3),
                    blurRadius: 8,
                    offset: const Offset(0, 2),
                  ),
                ],
              ),
              child: Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Flexible(
                    child: Text(
                      widget.message,
                      style: const TextStyle(color: Colors.white, fontSize: 13),
                    ),
                  ),
                  if (widget.actionLabel != null) ...[
                    const SizedBox(width: 12),
                    GestureDetector(
                      onTap: _onActionTap,
                      child: Text(
                        widget.actionLabel!,
                        style: const TextStyle(
                          color: Colors.white,
                          fontSize: 13,
                          fontWeight: FontWeight.bold,
                          decoration: TextDecoration.underline,
                        ),
                      ),
                    ),
                  ],
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}
