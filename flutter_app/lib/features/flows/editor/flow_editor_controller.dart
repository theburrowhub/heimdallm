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
  void _commit(ReviewFlow next, {Selection? selection}) {
    _pushUndo();
    state = _with(next, selection: selection);
  }

  void load(ReviewFlow flow) {
    _undo.clear();
    _redo.clear();
    _dragOrigin = null;
    final laid = autoLayout(flow, onlyMissing: true);
    state = FlowEditorState(flow: laid, saved: laid, check: checkGraph(laid));
  }

  /// Records [saved] (the flow the daemon accepted; the current flow when
  /// omitted) as the saved baseline. Edits made after it was sent stay dirty.
  void markSaved([ReviewFlow? saved]) =>
      state = _with(state.flow, saved: saved ?? state.flow);

  /// Changes only the selection: a test run's highlighted path stays.
  void select(Selection s) => _keep(selection: s);

  void rename(String name) => _commit(state.flow.copyWith(name: name));

  String addNode(FlowNodeType type, Offset at) {
    final id = state.flow.nextNodeId(type == FlowNodeType.agent ? 'a' : 'c');
    final node = FlowNode(
      id: id,
      type: type,
      position: at,
      schedule: type == FlowNodeType.condition
          ? const [ScheduleCondition()]
          : const [],
    );
    _commit(
      state.flow.copyWith(nodes: {...state.flow.nodes, id: node}),
      selection: NodeSelection(id),
    );
    return id;
  }

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
    if (n == null) return false;
    final validPort = n.isCondition
        ? (port == EdgePort.yes || port == EdgePort.no)
        : port == EdgePort.next;
    if (!validPort) return false;
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

  void arrange() => _commit(autoLayout(state.flow));

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
    final laid = autoLayout(keep, onlyMissing: true);
    state = _with(
      state.flow,
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
  );
}

final flowEditorProvider =
    NotifierProvider.autoDispose<FlowEditorController, FlowEditorState>(
      FlowEditorController.new,
    );
