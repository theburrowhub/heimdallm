import Cocoa
import XCTest
@testable import Heimdallm

@MainActor
final class RunnerTests: XCTestCase {
  func testAppDelegateKeepsTheTrayAppRunning() {
    let delegate = AppDelegate()

    XCTAssertFalse(
      delegate.applicationShouldTerminateAfterLastWindowClosed(NSApplication.shared)
    )
    XCTAssertTrue(delegate.applicationSupportsSecureRestorableState(NSApplication.shared))
  }
}
