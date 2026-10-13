import 'dart:async';
import 'dart:math' as math;
import 'dart:ui' show PointMode;

import 'package:flutter/gestures.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/canvas/canvas_geometry.dart';
import 'package:heimdallm/features/flows/canvas/canvas_overlays.dart';
import 'package:heimdallm/features/flows/canvas/edge_painter.dart';
import 'package:heimdallm/features/flows/canvas/node_widgets.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';
import 'package:heimdallm/features/flows/graph/flow_diff.dart';
import 'package:heimdallm/shared/design_system/color_resolver.dart';
import 'package:heimdallm/shared/design_system/tokens.dart';

/// Ports are drawn [kPortSize] wide but take pointers over this square.
const _portHit = 24.0;
const _cutSize = 28.0;
const _addSize = 18.0;

const _minScale = .3;
const _maxScale = 2.0;
const _zoomStep = 1.25;
const _boundary = 400.0;

/// Room left around the graph when it is fitted in view.
const _fitPadding = 48.0;

/// How far right of a step its exits' "+" buttons and hover area reach.
const _addReach = 44.0;

/// How far right of a step its exits' labels and "+" buttons are drawn.
const _exitReach = 64.0;

/// Room the overlays take from the view: the zoom controls on the right,
/// the lock banner on top while it shows. Fitting keeps the graph clear of
/// them.
EdgeInsets _overlayInsets(bool locked) =>
    EdgeInsets.fromLTRB(8, locked ? 76 : 8, 64, 8);

/// Lets the editor ask the canvas what part of the graph is in view, and
/// move the view.
class FlowCanvasController {
  _FlowCanvasState? _state;

  /// The canvas position at the centre of the visible viewport (after pan
  /// and zoom), or null before the canvas is laid out.
  Offset? get viewportCentre => _state?._viewportCentre();

  void zoomIn() => _state?._zoom(_zoomStep);
  void zoomOut() => _state?._zoom(1 / _zoomStep);

  /// Fits every step, Start included (the draft's while one is shown).
  void fit() => _state?._fit();
}

/// The flow graph on a pannable, zoomable canvas. While the assistant's
/// draft is shown, the draft is drawn (with removed steps as ghosts) and
/// editing is disabled until it is accepted or discarded.
class FlowCanvas extends ConsumerStatefulWidget {
  /// Called with the canvas position where a palette item was dropped.
  final void Function(FlowNodeType type, Offset at)? onDropFromPalette;

  /// Pan, zoom and select only: no moves, connections or deletes.
  final bool readOnly;

  /// Steps to mark as wrong besides the client check's (the daemon's
  /// last validation findings).
  final Set<String> errorNodes;

  final FlowCanvasController? controller;

  const FlowCanvas({
    super.key,
    this.onDropFromPalette,
    this.readOnly = false,
    this.errorNodes = const {},
    this.controller,
  });

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

  /// The hovered edge by identity; its geometry is looked up every build so
  /// the scissors follow the edge when its ends move.
  (String, EdgePort)? _hover;

  /// The last pointer position seen anywhere (global). A palette drag's
  /// DragTarget offset is the feedback's corner, not the pointer, so drops
  /// are placed from this instead.
  Offset? _lastPointer;

  /// The step ('' for Start) whose exits show their "+" buttons because the
  /// pointer is over it or them.
  String? _hoverStep;

  /// An edit was attempted while the canvas is locked: the banner says why.
  bool _flash = false;
  Timer? _flashTimer;

  @override
  void initState() {
    super.initState();
    GestureBinding.instance.pointerRouter.addGlobalRoute(_trackPointer);
    widget.controller?._state = this;
    // The flow was usually loaded before the canvas was built.
    _scheduleView(ViewRequest());
  }

  @override
  void didUpdateWidget(FlowCanvas old) {
    super.didUpdateWidget(old);
    if (old.controller != widget.controller) {
      if (old.controller?._state == this) old.controller!._state = null;
      widget.controller?._state = this;
    }
  }

  Offset? _viewportCentre() {
    final size = _viewSize();
    return size == null ? null : _transform.toScene(size.center(Offset.zero));
  }

  Size? _viewSize() {
    final box = context.findRenderObject() as RenderBox?;
    if (box == null || !box.hasSize || box.size.isEmpty) return null;
    return box.size;
  }

  /// What is drawn: the draft while one is shown, its removed steps too.
  (ReviewFlow, List<FlowNode>) _shown(FlowEditorState state) {
    final draft = state.draft;
    return (
      draft?.flow ?? state.flow,
      [
        if (draft != null)
          for (final e in draft.diff.nodes.entries)
            if (e.value == DiffKind.removed &&
                state.flow.nodes[e.key]?.position != null)
              state.flow.nodes[e.key]!,
      ],
    );
  }

  /// The canvas rectangle holding Start and every drawn step, with their
  /// exits' labels and "+" buttons.
  Rect _contentBounds() {
    final (shown, ghosts) = _shown(ref.read(flowEditorProvider));
    var r = kStartPosition & (kStartSize + const Offset(_exitReach, 0));
    for (final n in [...shown.nodes.values, ...ghosts]) {
      if (n.position != null) {
        r = r.expandToInclude(
          n.position! & (kNodeSize + const Offset(_exitReach, 0)),
        );
      }
    }
    return r;
  }

  /// The part of the view no overlay covers, in view coordinates.
  Rect _safeView(Size view) => _overlayInsets(
    !widget.readOnly && ref.read(flowEditorProvider).locked,
  ).deflateRect(Offset.zero & view);

  /// [r] (canvas coordinates) lies wholly in the uncovered part of the view.
  bool _inView(Rect r, Size view) {
    final safe = _safeView(view);
    final visible = Rect.fromPoints(
      _transform.toScene(safe.topLeft),
      _transform.toScene(safe.bottomRight),
    ).inflate(1);
    return visible.contains(r.topLeft) && visible.contains(r.bottomRight);
  }

  /// Sets the view to [scale] with [t] as translation, kept where the
  /// InteractiveViewer lets the operator pan from (the canvas plus its
  /// boundary margin covers the viewport).
  void _setView(double scale, Offset t, Size view) {
    final (shown, ghosts) = _shown(ref.read(flowEditorProvider));
    final canvas = canvasSizeFor([...shown.nodes.values, ...ghosts]);
    double clampAxis(double v, double viewLen, double canvasLen) {
      final hi = _boundary * scale;
      final lo = viewLen - (canvasLen + _boundary) * scale;
      return lo > hi ? hi : v.clamp(lo, hi);
    }

    _transform.value = Matrix4.diagonal3Values(scale, scale, scale)
      ..setTranslationRaw(
        clampAxis(t.dx, view.width, canvas.width),
        clampAxis(t.dy, view.height, canvas.height),
        0,
      );
  }

  /// Zooms by [factor] about the centre of the view.
  void _zoom(double factor) {
    final view = _viewSize();
    if (view == null) return;
    final centre = view.center(Offset.zero);
    final at = _transform.toScene(centre);
    final scale = (_transform.value.getMaxScaleOnAxis() * factor).clamp(
      _minScale,
      _maxScale,
    );
    _setView(scale, centre - at * scale, view);
  }

  /// Fits the whole graph in view, never zoomed in past 100 %. With
  /// [onlyIfHidden], leaves the view alone when everything is already in it.
  void _fit({bool onlyIfHidden = false}) {
    final view = _viewSize();
    if (view == null) return;
    final bounds = _contentBounds();
    if (onlyIfHidden && _inView(bounds, view)) return;
    final safe = _safeView(view);
    final padded = bounds.inflate(_fitPadding);
    final scale = math
        .min(safe.width / padded.width, safe.height / padded.height)
        .clamp(_minScale, 1.0);
    _setView(scale, safe.center - padded.center * scale, view);
  }

  /// Brings step [id] into view, centred, when any of it is out of view.
  void _reveal(String id) {
    final view = _viewSize();
    final pos = ref.read(flowEditorProvider).flow.nodes[id]?.position;
    if (view == null || pos == null) return;
    final r = pos & kNodeSize;
    if (_inView(r, view)) return;
    final scale = _transform.value.getMaxScaleOnAxis();
    _setView(scale, _safeView(view).center - r.center * scale, view);
  }

  /// Acts on [req] once the frame showing the change is laid out.
  void _scheduleView(ViewRequest? req) {
    if (req == null) return;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      final id = req.reveal;
      id == null ? _fit(onlyIfHidden: true) : _reveal(id);
    });
  }

  /// Asks which kind of step to add after [port] of [from], then adds it.
  /// The menu belongs to the canvas, not to the "+" (which goes away when
  /// the menu takes the hover from the canvas).
  Future<void> _openAddMenu(String from, EdgePort port, Offset at) async {
    final overlay =
        Overlay.of(context).context.findRenderObject()! as RenderBox;
    final local = overlay.globalToLocal(at);
    final type = await showMenu<FlowNodeType>(
      context: context,
      position: RelativeRect.fromLTRB(
        local.dx,
        local.dy,
        overlay.size.width - local.dx,
        overlay.size.height - local.dy,
      ),
      items: const [
        PopupMenuItem(
          key: ValueKey('port-add-agent'),
          value: FlowNodeType.agent,
          child: ListTile(
            dense: true,
            leading: Icon(Icons.smart_toy_outlined, size: 18),
            title: Text('Agent'),
          ),
        ),
        PopupMenuItem(
          key: ValueKey('port-add-condition'),
          value: FlowNodeType.condition,
          child: ListTile(
            dense: true,
            leading: Icon(Icons.call_split, size: 18),
            title: Text('Condition'),
          ),
        ),
      ],
    );
    if (type == null || !mounted || !_editable) return;
    _ctl.addConnected(from, port, type);
  }

  /// Draws the lock banner louder for a moment: an edit was attempted while
  /// the canvas is locked.
  void _flashLocked() {
    if (widget.readOnly || !ref.read(flowEditorProvider).locked) return;
    _flashTimer?.cancel();
    setState(() => _flash = true);
    _flashTimer = Timer(const Duration(milliseconds: 700), () {
      if (mounted) setState(() => _flash = false);
    });
  }

  @override
  void dispose() {
    _flashTimer?.cancel();
    if (widget.controller?._state == this) widget.controller!._state = null;
    GestureBinding.instance.pointerRouter.removeGlobalRoute(_trackPointer);
    _transform.dispose();
    _focus.dispose();
    super.dispose();
  }

  void _trackPointer(PointerEvent e) {
    if (e is PointerMoveEvent || e is PointerUpEvent || e is PointerDownEvent) {
      _lastPointer = e.position;
    }
  }

  bool get _editable =>
      !widget.readOnly && !ref.read(flowEditorProvider).locked;

  /// Ends any gesture in progress when editing becomes disabled (a draft
  /// arrived or is being drafted): the gesture's own callbacks will no longer
  /// act, so a node move so far becomes its own undo step and a port drag is
  /// dropped.
  void _abortGestures() {
    final node = _dragNode;
    if (node != null) _endNodeDrag(node);
    if (_dragPort != null || _hover != null) {
      setState(() {
        _dragPort = null;
        _dragTo = null;
        _hover = null;
      });
    }
  }

  /// Places a palette item centred under the pointer.
  void _dropFromPalette(DragTargetDetails<FlowNodeType> d) {
    if (!_editable) return;
    final global = _lastPointer ?? d.offset;
    final at =
        _toCanvas(global) - Offset(kNodeSize.width / 2, kNodeSize.height / 2);
    widget.onDropFromPalette?.call(d.data, at);
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

  void _startPortDrag(String from, EdgePort p, Offset global) {
    if (!_editable) return _flashLocked();
    setState(() {
      _dragPort = (from, p);
      _dragTo = _toCanvas(global);
    });
  }

  void _updatePortDrag(Offset global) {
    if (_dragPort == null) return;
    setState(() => _dragTo = _toCanvas(global));
  }

  void _cancelPortDrag() => setState(() {
    _dragPort = null;
    _dragTo = null;
  });

  void _startNodeDrag(String id, Offset global) {
    if (!_editable) return _flashLocked();
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

  static CanvasEdge? _edgeOf(List<CanvasEdge> edges, (String, EdgePort)? k) {
    if (k == null) return null;
    for (final e in edges) {
      if (e.from == k.$1 && e.port == k.$2) return e;
    }
    return null;
  }

  /// The step ('' for Start) whose card or exits are under [p].
  static String? _stepNear(ReviewFlow f, Offset p) {
    Rect reach(Offset pos, Size size) => Rect.fromLTRB(
      pos.dx,
      pos.dy - 8,
      pos.dx + size.width + _addReach,
      pos.dy + size.height + 8,
    );
    final ids = f.nodes.keys.toList()..sort();
    for (final id in ids.reversed) {
      final pos = f.nodes[id]!.position;
      if (pos != null && reach(pos, kNodeSize).contains(p)) return id;
    }
    return reach(kStartPosition, kStartSize).contains(p) ? '' : null;
  }

  void _onHover(
    Offset p,
    ReviewFlow shown,
    List<CanvasEdge> edges,
    bool editable,
  ) {
    final step = editable ? _stepNear(shown, p) : null;
    if (step != _hoverStep) setState(() => _hoverStep = step);
    // An edge passing under a step does not count: its scissors would sit
    // on the card and swallow the drag meant for it.
    final overNode = hitNode(shown, p) != null;
    final hit = editable && !overNode ? hitEdge(edges, p) : null;
    var h = hit == null ? null : (hit.from, hit.port);
    // Keep the scissors while the pointer is on them, even if that is a
    // little off the line.
    final current = _edgeOf(edges, _hover);
    if (h == null && current != null) {
      final mid = (current.a + current.b) / 2;
      if ((p - mid).distance <= _cutSize / 2 + 2) h = _hover;
    }
    if (h != _hover) setState(() => _hover = h);
  }

  @override
  Widget build(BuildContext context) {
    final state = ref.watch(flowEditorProvider);
    ref.listen(flowEditorProvider.select((s) => s.locked), (was, now) {
      if (now && was != true) _abortGestures();
    });
    final scheme = Theme.of(context).colorScheme;
    final yesColor = resolveAppColor(context, AppColors.success);
    final noColor = resolveAppColor(context, AppColors.danger);
    ref.listen(flowEditorProvider.select((s) => s.view), (_, req) {
      _scheduleView(req);
    });
    final (shown, ghosts) = _shown(state);
    final editable = !widget.readOnly && !state.locked;
    final locked = !widget.readOnly && state.locked;
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
    // The hovered edge as it is now; gone if it was cut or a draft is shown.
    final hover = editable ? _edgeOf(edges, _hover) : null;

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
          cursor: editable
              ? SystemMouseCursors.precise
              : (locked ? SystemMouseCursors.forbidden : MouseCursor.defer),
          child: GestureDetector(
            key: ValueKey(key),
            behavior: HitTestBehavior.opaque,
            // Callbacks stay set while a draft is shown (they check
            // editability themselves): removing them mid-gesture would
            // dispose the recognizer without an end or cancel.
            onPanStart: (d) => _startPortDrag(from, p, d.globalPosition),
            onPanUpdate: (d) => _updatePortDrag(d.globalPosition),
            onPanEnd: (_) => _endPortDrag(),
            onPanCancel: _cancelPortDrag,
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

    // A "+" right of an exit adds a step connected from it: always on an
    // open exit, on a connected one while the step is hovered.
    Widget addButton(String from, EdgePort p, Offset portCentre) {
      if (!editable || (usedPorts.contains((from, p)) && _hoverStep != from)) {
        return const SizedBox.shrink();
      }
      final c = portCentre + const Offset(_portHit / 2 + _addSize / 2 + 2, 0);
      return Positioned(
        left: c.dx - _addSize / 2,
        top: c.dy - _addSize / 2,
        child: Tooltip(
          message: 'Add a step here',
          child: MouseRegion(
            cursor: SystemMouseCursors.click,
            child: GestureDetector(
              key: ValueKey(
                from.isEmpty ? 'port-add-start' : 'port-add-$from-${p.name}',
              ),
              behavior: HitTestBehavior.opaque,
              onTapUp: (d) => _openAddMenu(from, p, d.globalPosition),
              child: Container(
                width: _addSize,
                height: _addSize,
                decoration: BoxDecoration(
                  color: canvasCardColor(scheme),
                  shape: BoxShape.circle,
                  border: Border.all(color: scheme.outline),
                ),
                child: Icon(
                  Icons.add,
                  size: 12,
                  color: scheme.onSurfaceVariant,
                ),
              ),
            ),
          ),
        ),
      );
    }

    final size = canvasSizeFor([...shown.nodes.values, ...ghosts]);
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
        onAcceptWithDetails: _dropFromPalette,
        builder: (context, _, _) => ColoredBox(
          color: scheme.surfaceContainerLow,
          child: Stack(
            children: [
              Positioned.fill(
                child: _viewer(
                  context,
                  MouseRegion(
                    // A pointer that appears over an edge (enter) counts as hover.
                    onEnter: (e) =>
                        _onHover(e.localPosition, shown, edges, editable),
                    onHover: (e) =>
                        _onHover(e.localPosition, shown, edges, editable),
                    onExit: (_) {
                      if (_hover != null || _hoverStep != null) {
                        setState(() {
                          _hover = null;
                          _hoverStep = null;
                        });
                      }
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
                        width: size.width,
                        height: size.height,
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
                            addButton('', EdgePort.start, startOutPort()),
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
                                locked: locked,
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
                                addButton(
                                  id,
                                  EdgePort.yes,
                                  outPort(shown.nodes[id]!, EdgePort.yes),
                                ),
                                port(
                                  'port-$id-no',
                                  outPort(shown.nodes[id]!, EdgePort.no),
                                  id,
                                  EdgePort.no,
                                  noColor,
                                  tooltip: 'no: otherwise',
                                ),
                                addButton(
                                  id,
                                  EdgePort.no,
                                  outPort(shown.nodes[id]!, EdgePort.no),
                                ),
                              ] else ...[
                                port(
                                  'port-$id-next',
                                  outPort(shown.nodes[id]!, EdgePort.next),
                                  id,
                                  EdgePort.next,
                                  scheme.outline,
                                  tooltip:
                                      'fallback: when this agent fails or '
                                      'runs out of quota',
                                ),
                                addButton(
                                  id,
                                  EdgePort.next,
                                  outPort(shown.nodes[id]!, EdgePort.next),
                                ),
                              ],
                            ],
                            if (hover != null) _cutButton(hover, scheme),
                          ],
                        ),
                      ),
                    ),
                  ),
                ),
              ),
              if (state.locked)
                Positioned(
                  top: 12,
                  left: 16,
                  right: 16,
                  child: Center(
                    child: CanvasLockedBanner(
                      drafting: state.drafting,
                      flashing: _flash,
                      onAccept: _ctl.acceptDraft,
                      onDiscard: _ctl.discardDraft,
                    ),
                  ),
                ),
              Positioned(
                right: 12,
                bottom: 12,
                child: CanvasZoomControls(
                  onZoomIn: () => _zoom(_zoomStep),
                  onZoomOut: () => _zoom(1 / _zoomStep),
                  onFit: _fit,
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }

  Widget _viewer(BuildContext context, Widget child) => InteractiveViewer(
    key: const ValueKey('flow-canvas'),
    transformationController: _transform,
    constrained: false,
    minScale: _minScale,
    maxScale: _maxScale,
    boundaryMargin: const EdgeInsets.all(_boundary),
    panEnabled: _dragPort == null,
    // No inertia: a fling stops where the pointer lets go. A step grabbed
    // while the canvas was still sliding moved with the canvas instead of
    // the pointer, and was dropped somewhere else.
    interactionEndFrictionCoefficient: double.minPositive,
    child: child,
  );

  Widget _node(
    FlowNode n,
    FlowEditorState state, {
    required bool editable,
    required bool locked,
    required bool selected,
    required bool onPath,
  }) => Positioned(
    left: n.position!.dx,
    top: n.position!.dy,
    child: MouseRegion(
      cursor: editable
          ? SystemMouseCursors.grab
          : (locked ? SystemMouseCursors.forbidden : MouseCursor.defer),
      child: GestureDetector(
        key: ValueKey('node-${n.id}'),
        // Grab where the pointer went down, not where the slop ran out.
        dragStartBehavior: DragStartBehavior.down,
        onTap: () {
          _focus.requestFocus();
          _ctl.select(NodeSelection(n.id));
          _flashLocked();
        },
        // Always set, see the port callbacks.
        onPanStart: (d) => _startNodeDrag(n.id, d.globalPosition),
        onPanUpdate: (d) => _updateNodeDrag(n.id, d.globalPosition),
        onPanEnd: (_) => _endNodeDrag(n.id),
        onPanCancel: () => _endNodeDrag(n.id),
        child: FlowNodeCard(
          node: n,
          selected: selected,
          hasError:
              state.check.errorNodes.contains(n.id) ||
              widget.errorNodes.contains(n.id),
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
