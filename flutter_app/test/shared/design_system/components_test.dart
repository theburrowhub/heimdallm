import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/design_system/components/components.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

Widget _hosted(Widget child, {bool dark = false}) {
  return MaterialApp(
    theme: dark ? HeimdallmTheme.dark() : HeimdallmTheme.light(),
    debugShowCheckedModeBanner: false,
    builder: (context, navigatorChild) =>
        HeimdallmTheme.scope(child: navigatorChild ?? const SizedBox.shrink()),
    home: Scaffold(body: Center(child: child)),
  );
}

void main() {
  testWidgets('AppText renders every role without error', (tester) async {
    await tester.pumpWidget(
      _hosted(
        const Column(
          children: [
            AppText.pageTitle('Page title'),
            AppText.sectionTitle('Section title'),
            AppText('Body text'),
            AppText.muted('Muted text'),
            AppText.label('LABEL'),
            AppText.mono('mono-text'),
          ],
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Page title'), findsOneWidget);
    expect(find.text('mono-text'), findsOneWidget);
  });

  testWidgets('AppSurface renders each elevation and honors bordered flag', (
    tester,
  ) async {
    await tester.pumpWidget(
      _hosted(
        Column(
          children: [
            const AppSurface(child: Text('canvas')),
            const AppSurface(
              elevation: AppSurfaceElevation.raised,
              bordered: false,
              child: Text('raised'),
            ),
          ],
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('canvas'), findsOneWidget);
    expect(find.text('raised'), findsOneWidget);
  });

  testWidgets('AppSurface honors an explicit padding override', (
    tester,
  ) async {
    await tester.pumpWidget(
      _hosted(
        const AppSurface(
          padding: EdgeInsets.all(20),
          child: Text('padded'),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('padded'), findsOneWidget);
  });

  testWidgets('AppButton fires onPressed and reflects disabled state', (
    tester,
  ) async {
    var tapped = false;
    await tester.pumpWidget(
      _hosted(
        AppButton(label: 'Save', onPressed: () => tapped = true),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.text('Save'));
    await tester.pump();
    expect(tapped, isTrue);

    await tester.pumpWidget(
      _hosted(const AppButton(label: 'Disabled', onPressed: null)),
    );
    await tester.pumpAndSettle();
    expect(find.text('Disabled'), findsOneWidget);
  });

  testWidgets('AppButton renders a leading icon', (tester) async {
    await tester.pumpWidget(
      _hosted(
        AppButton(
          label: 'Add',
          onPressed: () {},
          leading: const Icon(Icons.add),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Add'), findsOneWidget);
    expect(find.byIcon(Icons.add), findsOneWidget);
  });

  testWidgets('AppButton variants all render', (tester) async {
    await tester.pumpWidget(
      _hosted(
        Column(
          children: [
            AppButton.secondary(label: 'Secondary', onPressed: () {}),
            AppButton.subtle(label: 'Subtle', onPressed: () {}),
            AppButton.destructive(label: 'Delete', onPressed: () {}),
          ],
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Secondary'), findsOneWidget);
    expect(find.text('Subtle'), findsOneWidget);
    expect(find.text('Delete'), findsOneWidget);
  });

  testWidgets('AppBadge renders label with icon', (tester) async {
    await tester.pumpWidget(
      _hosted(
        const AppBadge(
          label: 'Open',
          foreground: Colors.white,
          background: Colors.green,
          icon: Icon(Icons.check),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Open'), findsOneWidget);
    expect(find.byIcon(Icons.check), findsOneWidget);
  });

  testWidgets('AppBadge honors an explicit border color', (tester) async {
    await tester.pumpWidget(
      _hosted(
        const AppBadge(
          label: 'Bordered',
          foreground: Colors.white,
          background: Colors.green,
          border: Colors.black,
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Bordered'), findsOneWidget);
  });

  testWidgets('components render under dark theme without error', (
    tester,
  ) async {
    await tester.pumpWidget(
      _hosted(
        Column(
          children: [
            const AppText.pageTitle('Dark title'),
            const AppSurface(child: Text('dark surface')),
            AppButton(label: 'Dark button', onPressed: () {}),
            const AppBadge(
              label: 'Dark badge',
              foreground: Colors.white,
              background: Colors.blue,
            ),
          ],
        ),
        dark: true,
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Dark title'), findsOneWidget);
    expect(find.text('dark surface'), findsOneWidget);
    expect(find.text('Dark button'), findsOneWidget);
    expect(find.text('Dark badge'), findsOneWidget);
  });
}
