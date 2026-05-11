import 'package:flutter/material.dart';

/// Wraps a TabBarView child so it survives tab switches.
///
/// Flutter's TabBarView disposes off-screen children by default, which under
/// Riverpod 3 means any provider whose only listener was that tab gets paused
/// (and `ref.invalidate` won't fire a rebuild without an active listener).
/// Re-entering the tab then renders an empty/stale frame while the chain
/// reactivates — surfaced as the "click twice to open" feel.
///
/// Wrapping each tab body in this widget pins the tab to the element tree via
/// AutomaticKeepAliveClientMixin, so its `ref.watch` calls stay active across
/// tab switches and the downstream providers never enter the paused state.
class KeepAliveTab extends StatefulWidget {
  final Widget child;
  const KeepAliveTab({super.key, required this.child});

  @override
  State<KeepAliveTab> createState() => _KeepAliveTabState();
}

class _KeepAliveTabState extends State<KeepAliveTab>
    with AutomaticKeepAliveClientMixin {
  @override
  bool get wantKeepAlive => true;

  @override
  Widget build(BuildContext context) {
    super.build(context);
    return widget.child;
  }
}
