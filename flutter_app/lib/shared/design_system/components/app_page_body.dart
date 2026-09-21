import 'package:flutter/widgets.dart';

/// Wraps a screen's body so it uses the full available width.
///
/// This is the one place the "every screen uses 100% of the available
/// width" rule lives: `Column[toolbar, header, Expanded(child)]`, with no
/// `ConstrainedBox` narrowing the content. Screens that previously capped
/// their width (Settings' `ConstrainedBox(maxWidth: 980)`) drop that cap in
/// favor of this wrapper.
class AppPageBody extends StatelessWidget {
  final Widget? toolbar;
  final Widget? header;
  final Widget child;

  const AppPageBody({
    super.key,
    this.toolbar,
    this.header,
    required this.child,
  });

  @override
  Widget build(BuildContext context) {
    return Column(
      children: [
        ?toolbar,
        ?header,
        Expanded(child: child),
      ],
    );
  }
}
