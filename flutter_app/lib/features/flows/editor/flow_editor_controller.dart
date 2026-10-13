import 'dart:convert';
import 'dart:math' as math;

import 'package:flutter/painting.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';
import 'package:heimdallm/features/flows/graph/flow_diff.dart';
import 'package:heimdallm/features/flows/graph/graph_validation.dart';

enum EdgePort { yes, no, next, start }

sealed class Selection {
  const Selection();
}

class NoSelection extends Selection {
  const NoSelection();
}

class NodeSelection extends Selection {
  final String id;
  const NodeSelection(this.id);
  @override
  bool operator ==(Object other) => other is NodeSelection && other.id == id;
  @override
  int get hashCode => id.hashCode;
}

class EdgeSelection extends Selection {
  final String from;
  final EdgePort port;
  const EdgeSelection(this.from, this.port);
  @override
  bool operator ==(Object other) =>
      other is EdgeSelection && other.from == from && other.port == port;
  @override
  int get hashCode => Object.hash(from, port);
}

/// Asks the canvas to move its view: to fit the graph when part of it is out
/// of view, or to bring the step [reveal] into view. Each request is a new
/// object, so the canvas acts on it once.
class ViewRequest {
  final String? reveal;
  ViewRequest({this.reveal});
}

/// An assistant result shown over the canvas until accepted or discarded.
class FlowDraft {
  final ReviewFlow flow;
  final String plan;
  final List<String> warnings;
  final FlowDiff diff;
  const FlowDraft({
    required this.flow,
    required this.plan,
    required this.warnings,
    required this.diff,
  });
}

class FlowEditorState {
  final ReviewFlow flow;
  final ReviewFlow saved;
  final Selection selection;
  final FlowDraft? draft;
  final GraphCheck check;
  final FlowDecision? simulation;

  /// The assistant is drafting: its answer will replace what is shown.
  final bool drafting;
  final int undoDepth;
  final int redoDepth;

  /// The latest view change asked of the canvas.
  final ViewRequest? view;

  const FlowEditorState({
    required this.flow,
    required this.saved,
    this.selection = const NoSelection(),
    this.draft,
    required this.check,
    this.simulation,
    this.drafting = false,
    this.undoDepth = 0,
    this.redoDepth = 0,
    this.view,
  });

  /// Editing is paused while a draft is shown or being drafted.
  bool get locked => draft != null || drafting;
  bool get dirty => jsonEncode(flow.toJson()) != jsonEncode(saved.toJson());
  bool get canUndo => undoDepth > 0;
  bool get canRedo => redoDepth > 0;
}

const _maxUndo = 100;
const _same = Object();

class FlowEditorController extends Notifier<FlowEditorState> {
  final _undo = <ReviewFlow>[];
  final _redo = <ReviewFlow>[];
  ReviewFlow? _dragOrigin;

  @override
  FlowEditorState build() {
    const empty = ReviewFlow();
    return FlowEditorState(flow: empty, saved: empty, check: checkGraph(empty));
  }

  FlowEditorState _with(
    ReviewFlow flow, {
    Selection? selection,
    FlowDraft? draft,
    bool clearDraft = false,
    ReviewFlow? saved,
    ViewRequest? view,
  }) => FlowEditorState(
    flow: flow,
    saved: saved ?? state.saved,
    selection: selection ?? state.selection,
    draft: clearDraft ? null : (draft ?? state.draft),
    check: checkGraph(flow),
    simulation: null,
    drafting: state.drafting,
    undoDepth: _undo.length,
    redoDepth: _redo.length,
    view: view ?? state.view,
  );

  /// Pushes the flow to restore on undo (the pre-drag flow while a drag is in
  /// progress), caps the stack and drops the redo history.
  void _pushUndo() {
    _undo.add(_dragOrigin ?? state.flow);
    _dragOrigin = null;
    if (_undo.length > _maxUndo) _undo.removeAt(0);
    _redo.clear();
  }

  /// The selection if it still points at something in [f], else none.
  Selection _validSelection(ReviewFlow f) {
    final sel = state.selection;
    final ok = switch (sel) {
      NodeSelection(:final id) => f.nodes.containsKey(id),
      EdgeSelection(:final from, :final port) =>
        port == EdgePort.start ? f.start.isNotEmpty : f.nodes.containsKey(from),
      NoSelection() => true,
    };
    return ok ? sel : const NoSelection();
  }

  /// Records the current flow for undo, then applies [next].
  void _commit(ReviewFlow next, {Selection? selection, ViewRequest? view}) {
    _pushUndo();
    state = _with(next, selection: selection, view: view);
  }

  void load(ReviewFlow flow) {
    _undo.clear();
    _redo.clear();
    _dragOrigin = null;
    final laid = layoutMissing(flow);
    state = FlowEditorState(
      flow: laid,
      saved: laid,
      check: checkGraph(laid),
      view: ViewRequest(),
    );
  }

  /// Records [saved] (the flow the daemon accepted; the current flow when
  /// omitted) as the saved baseline. Edits made after it was sent stay dirty.
  void markSaved([ReviewFlow? saved]) =>
      state = _with(state.flow, saved: saved ?? state.flow);

  /// Changes only the selection: a test run's highlighted path stays.
  void select(Selection s) => _keep(selection: s);

  void rename(String name) => _commit(state.flow.copyWith(name: name));

  /// A new step of [type] at [at], or below it if that spot is taken.
  FlowNode _newNode(FlowNodeType type, Offset at) => FlowNode(
    id: state.flow.nextNodeId(type == FlowNodeType.agent ? 'a' : 'c'),
    type: type,
    position: freeSpot(nodeRects(state.flow), at),
    schedule: type == FlowNodeType.condition
        ? const [ScheduleCondition()]
        : const [],
  );

  /// Adds a step at [at] (moved down until it overlaps nothing) and selects
  /// it.
  String addNode(FlowNodeType type, Offset at) {
    final node = _newNode(type, at);
    _commit(
      state.flow.copyWith(nodes: {...state.flow.nodes, node.id: node}),
      selection: NodeSelection(node.id),
      view: ViewRequest(reveal: node.id),
    );
    return node.id;
  }

  /// Adds a step of [type] to the right of [from] ('' for Start), connects
  /// it from [port] and selects it, as one undo step. Returns its id, or
  /// null when [from] has no such exit.
  String? addConnected(String from, EdgePort port, FlowNodeType type) {
    final f = state.flow;
    final Offset at;
    if (port == EdgePort.start) {
      at =
          kStartPosition +
          Offset(
            kStartSize.width + kColumnGap,
            (kStartSize.height - kNodeSize.height) / 2,
          );
    } else {
      final n = f.nodes[from];
      if (n == null || !_hasPort(n, port)) return null;
      final row = (kNodeSize.height + kRowGap) / 2;
      at =
          (n.position ?? Offset.zero) +
          Offset(kNodeSize.width + kColumnGap, switch (port) {
            EdgePort.yes => -row,
            EdgePort.no => row,
            _ => 0,
          });
    }
    final node = _newNode(type, at);
    final nodes = {...f.nodes, node.id: node};
    _commit(
      port == EdgePort.start
          ? f.copyWith(nodes: nodes, start: node.id)
          : f.copyWith(
              nodes: {...nodes, from: _setPort(f.nodes[from]!, port, node.id)},
            ),
      selection: NodeSelection(node.id),
      view: ViewRequest(reveal: node.id),
    );
    return node.id;
  }

  /// Points [port] of [from] ('' for Start) at [to], or at nothing when [to]
  /// is empty. The selection stays. False when the edge is not allowed.
  bool setEdge(String from, EdgePort port, String to) {
    final f = state.flow;
    final n = f.nodes[from];
    final current = port == EdgePort.start ? f.start : edgeTarget(n, port);
    if (current == to) return true;
    if (to.isNotEmpty) return connect(from, port, to);
    if (port == EdgePort.start) {
      _commit(f.copyWith(start: ''));
    } else if (n != null) {
      _commit(f.copyWith(nodes: {...f.nodes, from: _setPort(n, port, '')}));
    }
    return true;
  }

  static bool _hasPort(FlowNode n, EdgePort port) => n.isCondition
      ? (port == EdgePort.yes || port == EdgePort.no)
      : port == EdgePort.next;

  /// Moves [id] to [to], kept at non-negative coordinates: the canvas
  /// cannot reach a step above or left of its origin.
  void moveNode(String id, Offset to, {bool commit = true}) {
    final n = state.flow.nodes[id];
    if (n == null) return;
    final next = state.flow.copyWith(
      nodes: {
        ...state.flow.nodes,
        id: n.copyWith(
          position: Offset(math.max(0, to.dx), math.max(0, to.dy)),
        ),
      },
    );
    if (!commit) {
      _dragOrigin ??= state.flow;
      state = _with(next);
      return;
    }
    _commit(next);
  }

  bool connect(String from, EdgePort port, String to) {
    final f = state.flow;
    if (!f.nodes.containsKey(to)) return false;
    if (port == EdgePort.start) {
      _commit(f.copyWith(start: to));
      return true;
    }
    final n = f.nodes[from];
    if (n == null || !_hasPort(n, port)) return false;
    final cleared = f.copyWith(
      nodes: {...f.nodes, from: _setPort(n, port, '')},
    );
    if (wouldCreateCycle(cleared, from, to)) return false;
    _commit(f.copyWith(nodes: {...f.nodes, from: _setPort(n, port, to)}));
    return true;
  }

  void disconnect(String from, EdgePort port) {
    final f = state.flow;
    if (port == EdgePort.start) {
      _commit(f.copyWith(start: ''), selection: const NoSelection());
      return;
    }
    final n = f.nodes[from];
    if (n == null) return;
    _commit(
      f.copyWith(nodes: {...f.nodes, from: _setPort(n, port, '')}),
      selection: const NoSelection(),
    );
  }

  static FlowNode _setPort(FlowNode n, EdgePort port, String to) =>
      switch (port) {
        EdgePort.yes => n.copyWith(yes: to),
        EdgePort.no => n.copyWith(no: to),
        EdgePort.next => n.copyWith(next: to),
        EdgePort.start => n,
      };

  void deleteNode(String id) {
    final f = state.flow;
    if (!f.nodes.containsKey(id)) return;
    final nodes = {
      for (final e in f.nodes.entries)
        if (e.key != id) e.key: e.value.withoutEdgesTo(id),
    };
    _commit(
      f.copyWith(nodes: nodes, start: f.start == id ? '' : f.start),
      selection: const NoSelection(),
    );
  }

  void deleteSelection() {
    switch (state.selection) {
      case NodeSelection(:final id):
        deleteNode(id);
      case EdgeSelection(:final from, :final port):
        disconnect(from, port);
      case NoSelection():
        break;
    }
  }

  void updateNode(FlowNode node) {
    if (!state.flow.nodes.containsKey(node.id)) return;
    _commit(state.flow.copyWith(nodes: {...state.flow.nodes, node.id: node}));
  }

  void arrange() => _commit(autoLayout(state.flow), view: ViewRequest());

  void undo() {
    if (_dragOrigin != null) {
      // A drag in progress counts as a step: undo returns to before the drag.
      _undo.add(_dragOrigin!);
      _dragOrigin = null;
    }
    if (_undo.isEmpty) return;
    _redo.add(state.flow);
    final prev = _undo.removeLast();
    state = _with(prev, selection: _validSelection(prev));
  }

  void redo() {
    _dragOrigin = null;
    if (_redo.isEmpty) return;
    _undo.add(state.flow);
    final next = _redo.removeLast();
    state = _with(next, selection: _validSelection(next));
  }

  void showDraft(FlowAssistResult r) {
    // Keep the positions of steps the draft did not change, lay out the rest.
    final keep = r.flow.copyWith(
      nodes: {
        for (final e in r.flow.nodes.entries)
          e.key:
              e.value.position == null &&
                  state.flow.nodes[e.key]?.position != null
              ? e.value.copyWith(position: state.flow.nodes[e.key]!.position)
              : e.value,
      },
    );
    // Drafted steps also keep clear of the removed ones, drawn as ghosts.
    final laid = layoutMissing(
      keep,
      keepClearOf: [
        for (final n in state.flow.nodes.values)
          if (n.position != null && !keep.nodes.containsKey(n.id))
            n.position! & kNodeSize,
      ],
    );
    state = _with(
      state.flow,
      view: ViewRequest(),
      draft: FlowDraft(
        flow: laid,
        plan: r.plan,
        warnings: r.warnings,
        diff: diffFlows(state.flow, laid),
      ),
    );
  }

  void acceptDraft() {
    final d = state.draft;
    if (d == null) return;
    _pushUndo();
    state = _with(d.flow, clearDraft: true, selection: const NoSelection());
  }

  void discardDraft() => state = _with(state.flow, clearDraft: true);

  void setSimulation(FlowDecision? d) => _keep(simulation: d);

  void setDrafting(bool on) => _keep(drafting: on);

  /// The current state with only the simulation, the drafting flag or the
  /// selection changed.
  void _keep({
    Object? simulation = _same,
    bool? drafting,
    Selection? selection,
  }) => state = FlowEditorState(
    flow: state.flow,
    saved: state.saved,
    selection: selection ?? state.selection,
    draft: state.draft,
    check: state.check,
    simulation: identical(simulation, _same)
        ? state.simulation
        : simulation as FlowDecision?,
    drafting: drafting ?? state.drafting,
    undoDepth: _undo.length,
    redoDepth: _redo.length,
    view: state.view,
  );
}

/// Where [port] of [n] leads ('' when it leads nowhere).
String edgeTarget(FlowNode? n, EdgePort port) => switch (port) {
  EdgePort.yes => n?.yes ?? '',
  EdgePort.no => n?.no ?? '',
  EdgePort.next => n?.next ?? '',
  EdgePort.start => '',
};

/// The steps [port] of [from] ('' for Start) may lead to, in id order: any
/// step but [from] itself and those that would close a loop.
List<String> connectableTargets(ReviewFlow f, String from, EdgePort port) {
  final n = f.nodes[from];
  final cleared = n == null
      ? f
      : f.copyWith(
          nodes: {...f.nodes, from: FlowEditorController._setPort(n, port, '')},
        );
  return [
    for (final id in f.nodes.keys.toList()..sort())
      if (port == EdgePort.start || !wouldCreateCycle(cleared, from, id)) id,
  ];
}

final flowEditorProvider =
    NotifierProvider.autoDispose<FlowEditorController, FlowEditorState>(
      FlowEditorController.new,
    );
