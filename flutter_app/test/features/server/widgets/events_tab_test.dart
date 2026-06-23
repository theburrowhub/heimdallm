import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:heimdallm/core/api/sse_client.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/server/widgets/connection_status_banner.dart';
import 'package:heimdallm/features/server/widgets/events_tab.dart';

import '../../../core/platform/fake_platform_services.dart';

void main() {
  testWidgets(
    'EventsTab shows the reconnecting banner on a stream error and clears it '
    'when events resume',
    (tester) async {
      final platform = FakePlatformServices(apiBaseUrl: 'http://127.0.0.1:7842');
      // One controllable transport per HTTP attempt: the first carries the
      // error, the reconnect opens a second on which we deliver an event.
      final transports = <StreamController<List<int>>>[];
      final mockClient = MockClient.streaming((request, _) async {
        final controller = StreamController<List<int>>();
        transports.add(controller);
        return http.StreamedResponse(
          controller.stream,
          200,
          headers: const {'content-type': 'text/event-stream'},
        );
      });
      final client = SseClient(
        httpClient: mockClient,
        platform: platform,
        path: '/events',
        errorReconnectDelay: const Duration(milliseconds: 10),
      );
      addTearDown(() async {
        for (final t in transports) {
          if (!t.isClosed) await t.close();
        }
      });

      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            platformServicesProvider.overrideWithValue(platform),
          ],
          child: MaterialApp(home: Scaffold(body: EventsTab(client: client))),
        ),
      );
      await tester.pump(const Duration(milliseconds: 20));

      // Optimistic on connect: no banner before anything goes wrong.
      expect(transports, hasLength(1));
      expect(find.byType(ConnectionStatusBanner), findsNothing);

      // Transport drops → banner appears.
      transports[0].addError(Exception('socket closed'));
      await tester.pump(const Duration(milliseconds: 20));
      expect(find.byType(ConnectionStatusBanner), findsOneWidget);

      // Client reconnects (second attempt) and an event arrives → banner clears.
      await tester.pump(const Duration(milliseconds: 20));
      expect(transports, hasLength(2));
      transports[1].add(utf8.encode('event: polling_started\ndata: {}\n\n'));
      await tester.pump(const Duration(milliseconds: 20));
      expect(find.byType(ConnectionStatusBanner), findsNothing);

      // Dispose the tab so the SSE client cancels its pending reconnect timer.
      await tester.pumpWidget(const SizedBox());
    },
  );
}
