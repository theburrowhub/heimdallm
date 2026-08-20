import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/sse_client.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/logs/logs_screen.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

import '../../core/platform/fake_platform_services.dart';

void main() {
  testWidgets('LogsView returns to connected when logs resume', (tester) async {
    final platform = FakePlatformServices();
    final transports = <StreamController<List<int>>>[];
    final mockClient = MockClient.streaming((_, _) async {
      final transport = StreamController<List<int>>();
      transports.add(transport);
      return http.StreamedResponse(
        transport.stream,
        200,
        headers: const {'content-type': 'text/event-stream'},
      );
    });
    final client = SseClient(
      platform: platform,
      httpClient: mockClient,
      path: '/logs/stream',
      errorReconnectDelay: const Duration(milliseconds: 10),
    );
    addTearDown(() async {
      client.disconnect();
      for (final transport in transports) {
        if (!transport.isClosed) await transport.close();
      }
    });

    await tester.pumpWidget(
      ProviderScope(
        overrides: [platformServicesProvider.overrideWithValue(platform)],
        child: MaterialApp(
          home: Scaffold(body: LogsView(client: client)),
        ),
      ),
    );
    await tester.pump(const Duration(milliseconds: 20));

    expect(transports, hasLength(1));
    expect(_indicatorColor(tester), const Color(0xFF3FB950));

    transports.first.addError(StateError('socket closed'));
    await tester.pump();
    expect(_indicatorColor(tester), const Color(0xFFFF6B6B));

    await tester.pump(const Duration(milliseconds: 20));
    expect(transports, hasLength(2));
    transports[1].add(
      utf8.encode('event: log_line\ndata: {"line":"stream recovered"}\n\n'),
    );
    await tester.pump();

    expect(find.text('stream recovered'), findsOneWidget);
    expect(_indicatorColor(tester), const Color(0xFF3FB950));

    client.disconnect();
    await tester.pump();
    expect(_indicatorColor(tester), const Color(0xFFFF6B6B));

    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('LogsView creates its production stream client', (tester) async {
    final platform = FakePlatformServices(apiBaseUrl: 'http://127.0.0.1:1');

    await tester.pumpWidget(
      ProviderScope(
        overrides: [platformServicesProvider.overrideWithValue(platform)],
        child: const MaterialApp(home: Scaffold(body: LogsView())),
      ),
    );

    expect(_indicatorColor(tester), const Color(0xFF3FB950));

    await tester.pumpWidget(const SizedBox());
    await tester.pump();
  });
}

Color? _indicatorColor(WidgetTester tester) {
  final indicator = tester.widget<Container>(
    find.byKey(const ValueKey('logs-connection-indicator')),
  );
  return (indicator.decoration! as BoxDecoration).color;
}
