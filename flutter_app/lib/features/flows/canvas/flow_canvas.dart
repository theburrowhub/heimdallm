import 'dart:ui' show PointMode;

import 'package:flutter/gestures.dart' show DragStartBehavior;
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/canvas/canvas_geometry.dart';
import 'package:heimdallm/features/flows/canvas/edge_painter.dart';
import 'package:heimdallm/features/flows/canvas/node_widgets.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';
import 'package:heimdallm/features/flows/graph/flow_diff.dart';
import 'package:heimdallm/shared/design_system/color_resolver.dart';
import 'package:heimdallm/shared/design_system/tokens.dart';

const _canvasSize = Size(6000, 4000);

/// Ports are drawn [kPortSize] wide but take pointers over this square.
const _portHit = 24.0;
const _cutSize = 28.0;

/// The flow graph on a pannable, zoomable canvas. While the assistant's
/// draft is shown, the draft is drawn (with removed steps as ghosts) and
/// editing is disabled until it is accepted or discarded.
class FlowCanvas extends ConsumerStatefulWidget {
  /// Called with the canvas position where a palette item was dropped.
  final void Function(FlowNodeType type, Offset at)? onDropFromPalette;
  const FlowCanvas({super.key, this.onDropFromPalette});

  @override
  ConsumerState<FlowCanvas> createState() => _FlowCanvasState();
}

class _FlowCanvasState extends ConsumerState<FlowCanvas> {
  final _transform = TransformationController();
  final _focus = FocusNode(debugLabel: 'flow-canvas');
  final _stackKey = GlobalKey();

  /// The port an edge is being dragged from, and the pointer (canvas coords).
  (String, EdgePort)? _dragPort;
  Offset? _dragTo;

  /// The node being dragged, and where in the node it was grabbed.
  String? _dragNode;
  Offset _grab = Offset.zero;
  bool _nodeMoved = false;

  CanvasEdge? _hover;

  @override
  void dispose() {
    _transform.dispose();
    _focus.dispose();
    super.dispose();
  }

  /// Global pointer position → canvas coordinates (accounts for pan/zoom).
  Offset _toCanvas(Offset global) {
    final box = _stackKey.currentContext!.findRenderObject()! as RenderBox;
    return box.globalToLocal(global);
  }

  void _toast(String msg) => ScaffoldMessenger.maybeOf(
    context,
  )?.showSnackBar(SnackBar(content: Text(msg)));

  FlowEditorController get _ctl => ref.read(flowEditorProvider.notifier);

  void _endPortDrag() {
    final port = _dragPort;
    final to = _dragTo;
    setState(() {
      _dragPort = null;
      _dragTo = null;
    });
    if (port == null || to == null) return;
    final target = hitNode(ref.read(flowEditorProvider).flow, to);
    if (target == null) return;
    if (!_ctl.connect(port.$1, port.$2, target)) {
      _toast(
        'That connection would make the flow loop back, or the step has no such exit.',
      );
    }
  }

  void _cancelPortDrag() => setState(() {
    _dragPort = null;
    _dragTo = null;
  });

  void _startNodeDrag(String id, Offset global) {
    final pos = ref.read(flowEditorProvider).flow.nodes[id]?.position;
    if (pos == null) return;
    _dragNode = id;
    _grab = _toCanvas(global) - pos;
    _nodeMoved = false;
  }

  void _updateNodeDrag(String id, Offset global) {
    if (_dragNode != id) return;
    _nodeMoved = true;
    _ctl.moveNode(id, _toCanvas(global) - _grab, commit: false);
  }

  /// Ends a node drag, on release or cancel alike: whatever moved becomes
  /// one undo step, so a cancelled drag never leaves a pending step behind.
  void _endNodeDrag(String id) {
    if (_dragNode != id) return;
    _dragNode = null;
    if (!_nodeMoved) return;
    _nodeMoved = false;
    final pos = ref.read(flowEditorProvider).flow.nodes[id]?.position;
    if (pos != null) _ctl.moveNode(id, pos);
  }

  void _onHover(Offset p, List<CanvasEdge> edges, bool editable) {
    var h = editable ? hitEdge(edges, p) : null;
    // Keep the scissors while the pointer is on them, even if that is a
    // little off the line.
    if (h == null && _hover != null) {
      final mid = (_hover!.a + _hover!.b) / 2;
      if ((p - mid).distance <= _cutSize / 2 + 2) h = _hover;
    }
    if (h?.from != _hover?.from || h?.port != _hover?.port) {
      setState(() => _hover = h);
    }
  }

  @override
  Widget build(BuildContext context) {
    final state = ref.watch(flowEditorProvider);
    final scheme = Theme.of(context).colorScheme;
    final yesColor = resolveAppColor(context, AppColors.success);
    final noColor = resolveAppColor(context, AppColors.danger);
    final draft = state.draft;
    final shown = draft?.flow ?? state.flow;
    final editable = draft == null;
    final edges = canvasEdges(shown);
    final path = state.simulation?.path ?? const <FlowPathStep>[];
    final pathNodes = {for (final s in path) s.node};
    final pathEdges = <(String, EdgePort)>{
      if (path.isNotEmpty) ('', EdgePort.start),
    };
    for (var i = 0; i + 1 < path.length; i++) {
      final n = shown.nodes[path[i].node];
      if (n == null) continue;
      final port = n.isCondition
          ? ((path[i].holds ?? false) ? EdgePort.yes : EdgePort.no)
          : EdgePort.next;
      pathEdges.add((n.id, port));
    }
    final selection = state.selection;
    final selectedEdge = selection is EdgeSelection ? selection : null;
    final selectedNode = selection is NodeSelection ? selection.id : null;
    // Edges whose source port is in use, so the port dot shows as filled.
    final usedPorts = {for (final e in edges) (e.from, e.port)};
    // A stale hover (the edge was cut or a draft appeared) must not linger.
    final hover =
        editable &&
            _hover != null &&
            usedPorts.contains((_hover!.from, _hover!.port))
        ? _hover
        : null;

    Offset? dragFrom;
    if (_dragPort case (final from, final port)) {
      final n = shown.nodes[from];
      dragFrom = port == EdgePort.start
          ? startOutPort()
          : (n == null ? null : outPort(n, port));
    }

    Widget port(
      String key,
      Offset center,
      String from,
      EdgePort p,
      Color color, {
      String? tooltip,
    }) {
      Widget dot = PortDot(color: color, filled: usedPorts.contains((from, p)));
      if (tooltip != null) dot = Tooltip(message: tooltip, child: dot);
      return Positioned(
        left: center.dx - _portHit / 2,
        top: center.dy - _portHit / 2,
        child: MouseRegion(
          cursor: editable ? SystemMouseCursors.precise : MouseCursor.defer,
          child: GestureDetector(
            key: ValueKey(key),
            behavior: HitTestBehavior.opaque,
            onPanStart: editable
                ? (d) => setState(() {
                    _dragPort = (from, p);
                    _dragTo = _toCanvas(d.globalPosition);
                  })
                : null,
            onPanUpdate: editable
                ? (d) => setState(() => _dragTo = _toCanvas(d.globalPosition))
                : null,
            onPanEnd: editable ? (_) => _endPortDrag() : null,
            onPanCancel: editable ? _cancelPortDrag : null,
            child: SizedBox.square(
              dimension: _portHit,
              child: Center(child: dot),
            ),
          ),
        ),
      );
    }

    // Where edges enter: decorative, a drop lands on the whole card.
    Widget inDot(Offset nodePos) {
      final c = inPort(nodePos);
      return Positioned(
        left: c.dx - kPortSize / 2,
        top: c.dy - kPortSize / 2,
        child: IgnorePointer(child: PortDot(color: scheme.outline)),
      );
    }

    final ghosts = [
      if (draft != null)
        for (final e in draft.diff.nodes.entries)
          if (e.value == DiffKind.removed &&
              state.flow.nodes[e.key]?.position != null)
            state.flow.nodes[e.key]!,
    ];
    // Stacked in id order so painting matches hitNode's topmost rule.
    final ids =
        shown.nodes.keys
            .where((id) => shown.nodes[id]!.position != null)
            .toList()
          ..sort();

    return Focus(
      focusNode: _focus,
      autofocus: true,
      onKeyEvent: (node, event) {
        if (!editable || event is! KeyDownEvent) return KeyEventResult.ignored;
        if (event.logicalKey == LogicalKeyboardKey.delete ||
            event.logicalKey == LogicalKeyboardKey.backspace) {
          _ctl.deleteSelection();
          return KeyEventResult.handled;
        }
        return KeyEventResult.ignored;
      },
      child: DragTarget<FlowNodeType>(
        onWillAcceptWithDetails: (_) => editable,
        onAcceptWithDetails: (d) =>
            widget.onDropFromPalette?.call(d.data, _toCanvas(d.offset)),
        builder: (context, _, _) => ColoredBox(
          color: scheme.surfaceContainerLow,
          child: InteractiveViewer(
            key: const ValueKey('flow-canvas'),
            transformationController: _transform,
            constrained: false,
            minScale: .3,
            maxScale: 2,
            boundaryMargin: const EdgeInsets.all(400),
            panEnabled: _dragPort == null,
            child: MouseRegion(
              // A pointer that appears over an edge (enter) counts as hover.
              onEnter: (e) => _onHover(e.localPosition, edges, editable),
              onHover: (e) => _onHover(e.localPosition, edges, editable),
              onExit: (_) {
                if (_hover != null) setState(() => _hover = null);
              },
              child: GestureDetector(
                behavior: HitTestBehavior.opaque,
                onTapUp: (d) {
                  _focus.requestFocus();
                  final e = hitEdge(edges, d.localPosition);
                  _ctl.select(
                    e == null
                        ? const NoSelection()
                        : EdgeSelection(e.from, e.port),
                  );
                },
                child: SizedBox(
                  key: _stackKey,
                  width: _canvasSize.width,
                  height: _canvasSize.height,
                  child: Stack(
                    clipBehavior: Clip.none,
                    children: [
                      Positioned.fill(
                        child: RepaintBoundary(
                          child: CustomPaint(
                            painter: _DotGridPainter(
                              scheme.outlineVariant.withValues(alpha: .6),
                            ),
                          ),
                        ),
                      ),
                      Positioned.fill(
                        child: CustomPaint(
                          painter: EdgePainter(
                            edges: edges,
                            pathEdges: pathEdges,
                            selected: selectedEdge,
                            color: scheme.outline,
                            highlight: scheme.secondary,
                            selectedColor: scheme.primary,
                            yesColor: yesColor,
                            noColor: noColor,
                            labelBackground: canvasCardColor(scheme),
                            labelStyle:
                                Theme.of(context).textTheme.labelSmall ??
                                const TextStyle(fontSize: 11),
                            dragFrom: dragFrom,
                            dragTo: _dragTo,
                          ),
                        ),
                      ),
                      Positioned(
                        left: kStartPosition.dx,
                        top: kStartPosition.dy,
                        child: const StartNodeCard(),
                      ),
                      port(
                        'port-start',
                        startOutPort(),
                        '',
                        EdgePort.start,
                        scheme.primary,
                        tooltip: 'Drag to the first step',
                      ),
                      for (final g in ghosts)
                        Positioned(
                          left: g.position!.dx,
                          top: g.position!.dy,
                          child: IgnorePointer(
                            child: FlowNodeCard(
                              node: g,
                              selected: false,
                              hasError: false,
                              unreachable: false,
                              onPath: false,
                              diff: DiffKind.removed,
                              ghost: true,
                            ),
                          ),
                        ),
                      for (final id in ids) ...[
                        _node(
                          shown.nodes[id]!,
                          state,
                          editable: editable,
                          selected: selectedNode == id,
                          onPath: pathNodes.contains(id),
                        ),
                        inDot(shown.nodes[id]!.position!),
                        if (shown.nodes[id]!.isCondition) ...[
                          port(
                            'port-$id-yes',
                            outPort(shown.nodes[id]!, EdgePort.yes),
                            id,
                            EdgePort.yes,
                            yesColor,
                            tooltip: 'yes: when the condition holds',
                          ),
                          port(
                            'port-$id-no',
                            outPort(shown.nodes[id]!, EdgePort.no),
                            id,
                            EdgePort.no,
                            noColor,
                            tooltip: 'no: otherwise',
                          ),
                        ] else
                          port(
                            'port-$id-next',
                            outPort(shown.nodes[id]!, EdgePort.next),
                            id,
                            EdgePort.next,
                            scheme.outline,
                            tooltip: 'fallback: when this agent is unavailable',
                          ),
                      ],
                      if (hover != null) _cutButton(hover, scheme),
                    ],
                  ),
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }

  Widget _node(
    FlowNode n,
    FlowEditorState state, {
    required bool editable,
    required bool selected,
    required bool onPath,
  }) => Positioned(
    left: n.position!.dx,
    top: n.position!.dy,
    child: MouseRegion(
      cursor: editable ? SystemMouseCursors.grab : MouseCursor.defer,
      child: GestureDetector(
        key: ValueKey('node-${n.id}'),
        // Grab where the pointer went down, not where the slop ran out.
        dragStartBehavior: DragStartBehavior.down,
        onTap: () {
          _focus.requestFocus();
          _ctl.select(NodeSelection(n.id));
        },
        onPanStart: editable
            ? (d) => _startNodeDrag(n.id, d.globalPosition)
            : null,
        onPanUpdate: editable
            ? (d) => _updateNodeDrag(n.id, d.globalPosition)
            : null,
        onPanEnd: editable ? (_) => _endNodeDrag(n.id) : null,
        onPanCancel: editable ? () => _endNodeDrag(n.id) : null,
        child: FlowNodeCard(
          node: n,
          selected: selected,
          hasError: state.check.errorNodes.contains(n.id),
          unreachable: state.check.unreachable.contains(n.id),
          onPath: onPath,
          diff: state.draft?.diff.nodes[n.id],
        ),
      ),
    ),
  );

  Widget _cutButton(CanvasEdge e, ColorScheme scheme) {
    final mid = (e.a + e.b) / 2;
    return Positioned(
      left: mid.dx - _cutSize / 2,
      top: mid.dy - _cutSize / 2,
      child: Tooltip(
        message: 'Cut this connection',
        child: Material(
          key: const ValueKey('edge-cut'),
          color: canvasCardColor(scheme),
          shape: CircleBorder(side: BorderSide(color: scheme.outline)),
          elevation: 2,
          clipBehavior: Clip.antiAlias,
          child: InkWell(
            onTap: () {
              _ctl.disconnect(e.from, e.port);
              setState(() => _hover = null);
            },
            child: SizedBox.square(
              dimension: _cutSize,
              child: Icon(Icons.content_cut, size: 14, color: scheme.error),
            ),
          ),
        ),
      ),
    );
  }
}

/// A dot every 24 px, drawn in one call.
class _DotGridPainter extends CustomPainter {
  final Color color;
  _DotGridPainter(this.color);

  @override
  void paint(Canvas canvas, Size size) {
    final points = [
      for (var x = 0.0; x < size.width; x += 24)
        for (var y = 0.0; y < size.height; y += 24) Offset(x, y),
    ];
    canvas.drawPoints(
      PointMode.points,
      points,
      Paint()
        ..color = color
        ..strokeWidth = 2
        ..strokeCap = StrokeCap.round,
    );
  }

  @override
  bool shouldRepaint(_DotGridPainter old) => old.color != color;
}
