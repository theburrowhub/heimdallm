import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/features/issues/issues_screen.dart';

void main() {
  test('keeps otherwise-unreferenced production libraries in coverage', () {
    expect(const IssuesScreen(), isA<IssuesScreen>());
  });
}
