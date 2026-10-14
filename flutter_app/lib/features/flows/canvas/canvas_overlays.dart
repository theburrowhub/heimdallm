import 'package:flutter/material.dart';
import 'package:heimdallm/shared/design_system/color_resolver.dart';
import 'package:heimdallm/shared/design_system/components/components.dart';
import 'package:heimdallm/shared/design_system/tokens.dart';

/// Zoom in, zoom out and fit the whole graph, in a corner of the canvas.
class CanvasZoomControls extends StatelessWidget {
  final VoidCallback onZoomIn;
  final VoidCallback onZoomOut;
  final VoidCallback onFit;

  const CanvasZoomControls({
    super.key,
    required this.onZoomIn,
    required this.onZoomOut,
    required this.onFit,
  });

  @override
  Widget build(BuildContext context) {
    final border = resolveAppColor(context, AppColors.border);
    Widget button(String key, String tooltip, IconData icon, VoidCallback f) =>
        IconButton(
          key: ValueKey(key),
          tooltip: tooltip,
          visualDensity: VisualDensity.compact,
          iconSize: 18,
          icon: Icon(icon),
          onPressed: f,
        );
    return DecoratedBox(
      decoration: BoxDecoration(
        borderRadius: BorderRadius.circular(8),
        boxShadow: [
          BoxShadow(
            color: Colors.black.withValues(alpha: .12),
            blurRadius: 8,
            offset: const Offset(0, 2),
          ),
        ],
      ),
      child: AppSurface(
        elevation: AppSurfaceElevation.surface,
        padding: const EdgeInsets.all(2),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            button('canvas-zoom-in', 'Zoom in', Icons.add, onZoomIn),
            button('canvas-zoom-out', 'Zoom out', Icons.remove, onZoomOut),
            Container(width: 24, height: 1, color: border),
            button(
              'canvas-fit',
              'Fit the flow in view',
              Icons.fit_screen_outlined,
              onFit,
            ),
          ],
        ),
      ),
    );
  }
}

/// Says why the canvas ignores edits: an AI draft is under review (with its
/// Accept and Discard repeated here) or the agent is drafting. [flashing]
/// draws it louder for a moment after an edit was attempted.
class CanvasLockedBanner extends StatelessWidget {
  final bool drafting;
  final bool flashing;
  final VoidCallback? onAccept;
  final VoidCallback? onDiscard;

  const CanvasLockedBanner({
    super.key,
    required this.drafting,
    this.flashing = false,
    this.onAccept,
    this.onDiscard,
  });

  @override
  Widget build(BuildContext context) {
    final accent = resolveAppColor(context, AppColors.accent);
    final text = Theme.of(context).textTheme.bodyMedium;
    return AnimatedContainer(
      key: const ValueKey('canvas-locked-banner'),
      duration: const Duration(milliseconds: 150),
      padding: const EdgeInsets.fromLTRB(14, 6, 6, 6),
      decoration: BoxDecoration(
        color: flashing
            ? Color.alphaBlend(
                accent.withValues(alpha: .22),
                AppColors.surfaceRaised.resolve(context),
              )
            : AppColors.surfaceRaised.resolve(context),
        borderRadius: BorderRadius.circular(10),
        border: Border.all(
          color: accent.withValues(alpha: flashing ? 1 : .45),
          width: flashing ? 2 : 1,
        ),
        boxShadow: [
          BoxShadow(
            color: Colors.black.withValues(alpha: .12),
            blurRadius: 10,
            offset: const Offset(0, 2),
          ),
        ],
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(
            drafting ? Icons.auto_awesome : Icons.lock_outline,
            size: 16,
            color: accent,
          ),
          const SizedBox(width: 8),
          Flexible(
            child: Padding(
              padding: const EdgeInsets.symmetric(vertical: 6),
              child: Text(
                drafting
                    ? 'The agent is drafting — editing is paused'
                    : 'Reviewing the AI draft — accept or discard to edit',
                key: drafting ? const ValueKey('assist-drafting') : null,
                style: text?.copyWith(fontWeight: FontWeight.w500),
              ),
            ),
          ),
          if (!drafting) ...[
            const SizedBox(width: 12),
            OutlinedButton(
              key: const ValueKey('canvas-draft-discard'),
              onPressed: onDiscard,
              child: const Text('Discard'),
            ),
            const SizedBox(width: 6),
            FilledButton.icon(
              key: const ValueKey('canvas-draft-accept'),
              onPressed: onAccept,
              icon: const Icon(Icons.check, size: 18),
              label: const Text('Accept'),
            ),
          ] else
            const SizedBox(width: 8),
        ],
      ),
    );
  }
}
