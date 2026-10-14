import 'package:flutter/material.dart';
import 'package:heimdallm/features/flows/canvas/canvas_geometry.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';

/// Draws every edge with its label, the edges of a simulated path in the
/// highlight colour, the selected edge thicker, and the edge being dragged.
/// yes/no edges take [yesColor]/[noColor] so a branch reads at a glance.
class EdgePainter extends CustomPainter {
  final List<CanvasEdge> edges;
  final Set<(String, EdgePort)> pathEdges;
  final EdgeSelection? selected;
  final Color color;
  final Color highlight;
  final Color selectedColor;
  final Color yesColor;
  final Color noColor;
  final Color labelBackground;
  final TextStyle labelStyle;
  final Offset? dragFrom;
  final Offset? dragTo;

  EdgePainter({
    required this.edges,
    required this.pathEdges,
    required this.selected,
    required this.color,
    required this.highlight,
    required this.selectedColor,
    required this.yesColor,
    required this.noColor,
    required this.labelBackground,
    required this.labelStyle,
    this.dragFrom,
    this.dragTo,
  });

  @override
  void paint(Canvas canvas, Size size) {
    for (final e in edges) {
      final isSel = selected?.from == e.from && selected?.port == e.port;
      final onPath = pathEdges.contains((e.from, e.port));
      final base = switch (e.port) {
        EdgePort.yes => yesColor,
        EdgePort.no => noColor,
        _ => color,
      };
      final paint = Paint()
        ..style = PaintingStyle.stroke
        ..strokeCap = StrokeCap.round
        ..strokeWidth = isSel ? 3 : (onPath ? 2.5 : 1.75)
        ..color = isSel ? selectedColor : (onPath ? highlight : base);
      canvas.drawPath(edgePath(e.a, e.b), paint);
      _arrow(canvas, e.b, paint.color);
      final label = switch (e.port) {
        EdgePort.yes => 'yes',
        EdgePort.no => 'no',
        EdgePort.next => 'fallback',
        EdgePort.start => '',
      };
      if (label.isNotEmpty) _label(canvas, label, e, paint.color);
    }
    if (dragFrom != null && dragTo != null) {
      canvas.drawPath(
        edgePath(dragFrom!, dragTo!),
        Paint()
          ..style = PaintingStyle.stroke
          ..strokeCap = StrokeCap.round
          ..strokeWidth = 2
          ..color = selectedColor,
      );
      canvas.drawCircle(dragTo!, 4, Paint()..color = selectedColor);
    }
  }

  /// A small pill just past the port, above the line for yes/fallback and
  /// below it for no, so the two exits of a condition never collide.
  void _label(Canvas canvas, String text, CanvasEdge e, Color c) {
    final tp = TextPainter(
      text: TextSpan(
        text: text,
        style: labelStyle.copyWith(color: c, fontWeight: FontWeight.w600),
      ),
      textDirection: TextDirection.ltr,
    )..layout();
    const pad = EdgeInsets.symmetric(horizontal: 5, vertical: 1);
    final w = tp.width + pad.horizontal;
    final h = tp.height + pad.vertical;
    final dy = e.port == EdgePort.no ? 4.0 : -h - 4;
    final box = Rect.fromLTWH(e.a.dx + 12, e.a.dy + dy, w, h);
    final r = RRect.fromRectAndRadius(box, Radius.circular(h / 2));
    canvas.drawRRect(r, Paint()..color = labelBackground);
    canvas.drawRRect(
      r,
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = 1
        ..color = c.withValues(alpha: .5),
    );
    tp.paint(canvas, box.topLeft + Offset(pad.left, pad.top));
  }

  /// An arrowhead ending just left of the in-port dot drawn over [tip].
  void _arrow(Canvas canvas, Offset tip, Color c) {
    final t = tip - const Offset(7, 0);
    final p = Path()
      ..moveTo(t.dx, t.dy)
      ..lineTo(t.dx - 8, t.dy - 5)
      ..lineTo(t.dx - 8, t.dy + 5)
      ..close();
    canvas.drawPath(p, Paint()..color = c);
  }

  @override
  bool shouldRepaint(EdgePainter old) =>
      old.edges != edges ||
      old.pathEdges != pathEdges ||
      old.selected != selected ||
      old.dragFrom != dragFrom ||
      old.dragTo != dragTo ||
      old.color != color ||
      old.highlight != highlight ||
      old.selectedColor != selectedColor ||
      old.yesColor != yesColor ||
      old.noColor != noColor ||
      old.labelBackground != labelBackground ||
      old.labelStyle != labelStyle;
}
