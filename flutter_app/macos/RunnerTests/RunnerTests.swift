import Cocoa
import Darwin
@testable import Heimdallm
import XCTest

private enum BoundaryFixtureError: Error {
  case failed
}

class RunnerTests: XCTestCase {
  private var temporaryDirectory: URL!

  override func setUpWithError() throws {
    temporaryDirectory = FileManager.default.temporaryDirectory.appendingPathComponent(
      "heimdallm-native-tests-\(UUID().uuidString)",
      isDirectory: true
    )
    try FileManager.default.createDirectory(
      at: temporaryDirectory,
      withIntermediateDirectories: false,
      attributes: [.posixPermissions: 0o700]
    )
  }

  override func tearDownWithError() throws {
    if let temporaryDirectory {
      try FileManager.default.removeItem(at: temporaryDirectory)
    }
  }

  func testJournalRoundTripIsPrivateAndAtomic() throws {
    let url = temporaryDirectory.appendingPathComponent("app-update-recovery.json")
    let store = UpdateRecoveryJournalStore(url: url)
    let first = pendingJournal(version: "1.2.3")
    try store.write(first)

    XCTAssertEqual(try store.read(), first)
    var info = stat()
    XCTAssertEqual(lstat(url.path, &info), 0)
    XCTAssertEqual(info.st_mode & 0o777, 0o600)
    XCTAssertFalse(
      try FileManager.default.contentsOfDirectory(atPath: temporaryDirectory.path)
        .contains { $0.hasSuffix(".tmp") }
    )

    var second = stoppedJournal(version: "1.2.4", phase: .preparing)
    try store.write(second)
    XCTAssertEqual(try store.read(), second)
    XCTAssertEqual(try store.read()?.daemonBootID, "boot-a")
    XCTAssertEqual(try store.read()?.daemonVersion, "1.2.4")

    second.phase = .sealed
    try store.write(second)
    XCTAssertEqual(try store.read(), second)

    second.phase = .installing
    try store.write(second)
    XCTAssertEqual(try store.read(), second)
    try store.clear()
    XCTAssertNil(try store.read())
  }

  func testJournalIgnoresUncommittedTemporaryFile() throws {
    let url = temporaryDirectory.appendingPathComponent("app-update-recovery.json")
    let store = UpdateRecoveryJournalStore(url: url)
    let journal = pendingJournal(version: "2.0.0")
    try store.write(journal)
    try Data("truncated".utf8).write(
      to: temporaryDirectory.appendingPathComponent(
        ".app-update-recovery.json.interrupted.tmp"
      )
    )

    XCTAssertEqual(try store.read(), journal)
  }

  func testAbsentDaemonSealIsGoCompatiblePrivateAndLockExclusive() throws {
    let lockURL = temporaryDirectory.appendingPathComponent("daemon.lock")
    let markerURL = temporaryDirectory.appendingPathComponent("update-drain.json")
    let boundary = UpdateAbsentDaemonSealBoundary(lockURL: lockURL, markerURL: markerURL)
    let leaseID = UUID().uuidString.lowercased()

    let competingLock = Darwin.open(
      lockURL.path,
      O_RDWR | O_CREAT | O_NOFOLLOW | O_CLOEXEC,
      S_IRUSR | S_IWUSR
    )
    XCTAssertGreaterThanOrEqual(competingLock, 0)
    XCTAssertEqual(flock(competingLock, LOCK_EX | LOCK_NB), 0)
    XCTAssertThrowsError(try boundary.seal(leaseID: leaseID))
    XCTAssertFalse(FileManager.default.fileExists(atPath: markerURL.path))
    XCTAssertEqual(flock(competingLock, LOCK_UN), 0)
    XCTAssertEqual(Darwin.close(competingLock), 0)

    try boundary.seal(leaseID: leaseID)
    let object = try XCTUnwrap(
      JSONSerialization.jsonObject(with: Data(contentsOf: markerURL)) as? [String: Any]
    )
    XCTAssertEqual(Set(object.keys), Set(["lease_id", "sealed"]))
    XCTAssertEqual(object["lease_id"] as? String, leaseID)
    XCTAssertEqual(object["sealed"] as? Bool, true)

    var info = stat()
    XCTAssertEqual(lstat(markerURL.path, &info), 0)
    XCTAssertEqual(info.st_mode & 0o777, 0o600)
    XCTAssertFalse(
      try FileManager.default.contentsOfDirectory(atPath: temporaryDirectory.path)
        .contains { $0.hasPrefix(".update-drain.json.") }
    )

    // Go omits `sealed` for an ordinary lease. The same owner may atomically
    // upgrade that marker; a foreign owner may never be overwritten.
    let unsealed = try JSONSerialization.data(withJSONObject: ["lease_id": leaseID])
    try unsealed.write(to: markerURL)
    XCTAssertEqual(chmod(markerURL.path, 0o600), 0)
    try boundary.seal(leaseID: leaseID)
    let upgraded = try XCTUnwrap(
      JSONSerialization.jsonObject(with: Data(contentsOf: markerURL)) as? [String: Any]
    )
    XCTAssertEqual(upgraded["sealed"] as? Bool, true)
    XCTAssertThrowsError(try boundary.seal(leaseID: UUID().uuidString))

    let targetURL = temporaryDirectory.appendingPathComponent("foreign-marker.json")
    try Data("{\"lease_id\":\"(leaseID)\"}".utf8).write(to: targetURL)
    XCTAssertEqual(chmod(targetURL.path, 0o600), 0)
    try FileManager.default.removeItem(at: markerURL)
    XCTAssertEqual(symlink(targetURL.path, markerURL.path), 0)
    XCTAssertThrowsError(try boundary.seal(leaseID: leaseID))
  }

  func testJournalRejectsCorruptionAndUnsafePermissions() throws {
    let url = temporaryDirectory.appendingPathComponent("app-update-recovery.json")
    let store = UpdateRecoveryJournalStore(url: url)
    try Data("{".utf8).write(to: url)
    XCTAssertThrowsError(try store.read())

    try store.write(pendingJournal(version: "3.0.0"))
    XCTAssertEqual(chmod(url.path, 0o644), 0)
    XCTAssertThrowsError(try store.read())
  }

  func testJournalValidatesLeaseAndPhaseTransitions() throws {
    let store = UpdateRecoveryJournalStore(
      url: temporaryDirectory.appendingPathComponent("app-update-recovery.json")
    )
    let invalidLease = UpdateRecoveryJournal(
      schemaVersion: UpdateRecoveryJournalStore.currentSchemaVersion,
      expectedVersion: "1.0.0",
      phase: .pendingInstall,
      leaseID: "shared-unowned-lease",
      daemonPID: nil,
      launchAgentWasLoaded: nil,
      launchAgentWasDisabled: nil
    )
    XCTAssertThrowsError(try store.validate(invalidLease))

    let incompleteStop = UpdateRecoveryJournal(
      schemaVersion: UpdateRecoveryJournalStore.currentSchemaVersion,
      expectedVersion: "1.0.0",
      phase: .preparing,
      leaseID: UUID().uuidString,
      daemonPID: 42,
      launchAgentWasLoaded: nil,
      launchAgentWasDisabled: nil
    )
    XCTAssertThrowsError(try store.validate(incompleteStop))

    let pollutedPending = UpdateRecoveryJournal(
      schemaVersion: UpdateRecoveryJournalStore.currentSchemaVersion,
      expectedVersion: "1.0.0",
      phase: .pendingInstall,
      leaseID: UUID().uuidString,
      daemonPID: 42,
      launchAgentWasLoaded: true,
      launchAgentWasDisabled: false
    )
    XCTAssertThrowsError(try store.validate(pollutedPending))
    XCTAssertNoThrow(try store.validate(stoppedJournal(version: "1.0.0", phase: .sealed)))
    XCTAssertNoThrow(try store.validate(stoppedJournal(version: "1.0.0", phase: .installing)))

    let returned = stoppedJournal(version: "1.0.0", phase: .installing)
      .returnedToPendingInstall()
    XCTAssertEqual(returned.phase, .pendingInstall)
    XCTAssertNil(returned.daemonPID)
    XCTAssertNil(returned.daemonBootID)
    XCTAssertNil(returned.daemonVersion)
    XCTAssertNil(returned.launchAgentWasLoaded)
    XCTAssertNil(returned.launchAgentWasDisabled)
    XCTAssertNoThrow(try store.validate(returned))
  }

  func testLaunchAgentSnapshotParsesQuotedIdentity() throws {
    let snapshot = try LaunchAgentSnapshot.parse(
      launchctlPrint: """
        gui/501/com.heimdallm.daemon = {
          path = "/Users/example/Library/LaunchAgents/com.heimdallm.daemon.plist"
          program = "/Applications/Heimdallm.app/Contents/MacOS/heimdalld"
          pid = 4242
        }
        """
    )

    XCTAssertEqual(
      snapshot,
      LaunchAgentSnapshot(
        plistPath: "/Users/example/Library/LaunchAgents/com.heimdallm.daemon.plist",
        programPath: "/Applications/Heimdallm.app/Contents/MacOS/heimdalld",
        pid: 4242
      )
    )
  }

  func testLaunchAgentSnapshotRejectsMissingOrInvalidPID() {
    XCTAssertThrowsError(
      try LaunchAgentSnapshot.parse(
        launchctlPrint: "path = /tmp/service.plist\nprogram = /tmp/heimdalld\npid = 0"
      )
    )
    XCTAssertThrowsError(
      try LaunchAgentSnapshot.parse(
        launchctlPrint: "path = /tmp/service.plist\nprogram = /tmp/heimdalld"
      )
    )
  }

  func testTokenProviderRereadsPathAndNeverFallsBackToStaleInlineToken() throws {
    let tokenURL = temporaryDirectory.appendingPathComponent("api_token")
    try Data("current-token\n".utf8).write(to: tokenURL)
    let provider = UpdateAPITokenProvider(path: tokenURL, fallback: "stale-inline-token")
    XCTAssertEqual(provider.current(), "current-token")

    try Data("rotated-token\n".utf8).write(to: tokenURL)
    XCTAssertEqual(provider.current(), "rotated-token")

    try FileManager.default.removeItem(at: tokenURL)
    XCTAssertNil(provider.current())
  }

  @MainActor
  func testColdJournalDeniesQuitAndCompletedStateAllowsTermination() throws {
    let url = temporaryDirectory.appendingPathComponent("app-update-recovery.json")
    let store = UpdateRecoveryJournalStore(url: url)
    try store.write(pendingJournal(version: "4.0.0"))

    let coordinator = DaemonUpdateCoordinator(initialDataDirectory: temporaryDirectory)
    XCTAssertTrue(coordinator.hasPendingUpdate)
    XCTAssertEqual(coordinator.applicationShouldTerminate(), .terminateCancel)

    let duplicate = DaemonUpdateCoordinator(initialDataDirectory: temporaryDirectory)
    duplicate.allowDuplicateInstanceTermination()
    XCTAssertEqual(duplicate.applicationShouldTerminate(), .terminateNow)

    // This is the final in-memory transition used only after daemon identity,
    // DELETE acknowledgement, and durable journal removal have succeeded.
    try store.clear()
    coordinator.resetAfterCompletedUpdateState()
    XCTAssertFalse(coordinator.hasPendingUpdate)
    XCTAssertEqual(coordinator.applicationShouldTerminate(), .terminateNow)
  }

  @MainActor
  func testRecoveryFailureRetainsJournalButAllowsControlledQuit() throws {
    let store = UpdateRecoveryJournalStore(
      url: temporaryDirectory.appendingPathComponent("app-update-recovery.json")
    )
    let journal = pendingJournal(version: "4.1.0")
    try store.write(journal)

    let coordinator = DaemonUpdateCoordinator(initialDataDirectory: temporaryDirectory)
    XCTAssertEqual(coordinator.applicationShouldTerminate(), .terminateCancel)

    coordinator.allowTerminationAfterRecoveryFailure()
    XCTAssertEqual(try store.read(), journal)
    XCTAssertEqual(coordinator.applicationShouldTerminate(), .terminateNow)
  }

  func testNativeUpdateCapabilityCannotBeReenabledByDart() {
    XCTAssertFalse(
      DaemonUpdateCoordinator.effectiveNativeUpdatesEnabled(
        buildTrustAllows: false,
        requested: true
      )
    )
    XCTAssertFalse(
      DaemonUpdateCoordinator.effectiveNativeUpdatesEnabled(
        buildTrustAllows: true,
        requested: false
      )
    )
    XCTAssertTrue(
      DaemonUpdateCoordinator.effectiveNativeUpdatesEnabled(
        buildTrustAllows: true,
        requested: true
      )
    )
  }

  func testInstalledVersionOrderingDistinguishesUpgradeFromDowngrade() {
    XCTAssertEqual(
      DaemonUpdateCoordinator.compareInstalledVersion("0.8.3", expected: "0.8.4"),
      .orderedAscending
    )
    XCTAssertEqual(
      DaemonUpdateCoordinator.compareInstalledVersion("0.8.4", expected: "0.8.4"),
      .orderedSame
    )
    XCTAssertEqual(
      DaemonUpdateCoordinator.compareInstalledVersion("0.8.5", expected: "0.8.4"),
      .orderedDescending
    )
  }

  func testDeferredReplacementSealsObservedOldDaemonBeforeRestoringNewBundle() throws {
    XCTAssertEqual(
      try UpdateDaemonVersionPolicy.deferredRunningVersion(
        statusVersion: "0.8.3",
        replacementTargetVersion: "0.8.4"
      ),
      "0.8.3"
    )
  }

  func testNormalReplacementRejectsMixedAppAndDaemonVersions() {
    XCTAssertThrowsError(
      try UpdateDaemonVersionPolicy.normalRunningVersion(
        statusVersion: "0.8.3",
        currentBundleVersion: "0.8.4"
      )
    ) { error in
      XCTAssertEqual(
        error as? UpdateDaemonVersionPolicyError,
        .normalVersionMismatch(expected: "0.8.4", actual: "0.8.3")
      )
    }
  }

  func testInterruptedPreparationRebindsLaunchdRespawnIdentity() throws {
    let original = stoppedJournal(version: "4.2.0", phase: .preparing)
    let result = try UpdateInterruptedPreparationBoundary.reboundJournal(
      original,
      installedVersion: "4.2.0",
      daemonPID: 9001,
      daemonBootID: "boot-b",
      leaseID: original.leaseID,
      state: "ready",
      activeTotal: 0,
      daemonVersion: "4.2.0",
      bootstrapAuthorized: false
    )
    let rebound = result.journal

    XCTAssertFalse(result.replacementAlreadyRunning)
    XCTAssertEqual(rebound.phase, .preparing)
    XCTAssertEqual(rebound.daemonPID, 9001)
    XCTAssertEqual(rebound.daemonBootID, "boot-b")
    XCTAssertEqual(rebound.daemonVersion, "4.2.0")
    XCTAssertEqual(rebound.launchAgentWasLoaded, original.launchAgentWasLoaded)

    let store = UpdateRecoveryJournalStore(
      url: temporaryDirectory.appendingPathComponent("app-update-recovery.json")
    )
    try store.write(rebound)
    XCTAssertEqual(try store.read(), rebound)
  }

  func testDeferredPreparationAdoptsOnlyExactInstalledRespawn() throws {
    let deferred = UpdateRecoveryJournal(
      schemaVersion: UpdateRecoveryJournalStore.currentSchemaVersion,
      expectedVersion: "0.8.4",
      phase: .preparing,
      leaseID: UUID().uuidString,
      daemonPID: 4242,
      daemonBootID: "boot-old",
      daemonVersion: "0.8.3",
      launchAgentWasLoaded: true,
      launchAgentWasDisabled: false
    )
    let result = try UpdateInterruptedPreparationBoundary.reboundJournal(
      deferred,
      installedVersion: "0.8.4",
      daemonPID: 9001,
      daemonBootID: "boot-new",
      leaseID: deferred.leaseID,
      state: "ready",
      activeTotal: 0,
      daemonVersion: "0.8.4",
      bootstrapAuthorized: false
    )

    XCTAssertTrue(result.replacementAlreadyRunning)
    XCTAssertEqual(result.journal.daemonPID, 9001)
    XCTAssertEqual(result.journal.daemonBootID, "boot-new")
    XCTAssertEqual(result.journal.daemonVersion, "0.8.4")

    XCTAssertThrowsError(
      try UpdateInterruptedPreparationBoundary.reboundJournal(
        deferred,
        installedVersion: "0.8.4",
        daemonPID: 9002,
        daemonBootID: "boot-third",
        leaseID: deferred.leaseID,
        state: "ready",
        activeTotal: 0,
        daemonVersion: "0.8.5",
        bootstrapAuthorized: false
      )
    )
  }

  @MainActor
  func testDrainDeadlineCancelsAStalledOperation() async {
    var cancelled = false
    do {
      let _: String = try await UpdateDrainDeadline.execute(
        timeoutNanoseconds: 20_000_000
      ) {
        do {
          try await Task.sleep(nanoseconds: 5_000_000_000)
          return "unexpected"
        } catch is CancellationError {
          cancelled = true
          throw CancellationError()
        }
      }
      XCTFail("stalled drain unexpectedly crossed its global deadline")
    } catch UpdateDrainDeadlineError.timedOut {
      XCTAssertTrue(cancelled)
    } catch {
      XCTFail("unexpected error: \(error)")
    }
  }

  @MainActor
  func testRecoveryDeadlineCancelsAStalledProbe() async {
    var cancelled = false
    do {
      let _: String = try await UpdateRecoveryDeadline.execute(
        timeoutNanoseconds: 20_000_000
      ) {
        do {
          try await Task.sleep(nanoseconds: 5_000_000_000)
          return "unexpected"
        } catch is CancellationError {
          cancelled = true
          throw CancellationError()
        }
      }
      XCTFail("stalled recovery unexpectedly crossed its global deadline")
    } catch UpdateRecoveryDeadlineError.timedOut {
      XCTAssertTrue(cancelled)
    } catch {
      XCTFail("unexpected error: \(error)")
    }
  }

  @MainActor
  func testRecoveryPollingDoesNotStopAtLegacyAttemptCap() async throws {
    var attempts = 0
    let result: String = try await UpdateRecoveryDeadline.execute(
      timeoutNanoseconds: 1_000_000_000
    ) {
      try await UpdateRecoveryPolling.untilSuccess(delayNanoseconds: 0) {
        attempts += 1
        if attempts <= 205 { throw BoundaryFixtureError.failed }
        return "ready"
      }
    }

    XCTAssertEqual(result, "ready")
    XCTAssertEqual(attempts, 206)
  }

  @MainActor
  func testFlutterCallbackDeadlineRejectsMissingReply() async {
    do {
      try await UpdateCallbackDeadline.execute(timeoutNanoseconds: 20_000_000) { _ in }
      XCTFail("missing Flutter callback unexpectedly completed")
    } catch UpdateCallbackDeadlineError.timedOut {
      // Expected: recovery returns fail-closed instead of waiting forever.
    } catch {
      XCTFail("unexpected error: \(error)")
    }
  }

  @MainActor
  func testReplacementBoundaryPersistsThenSealsBeforeStop() async throws {
    var events: [String] = []
    try await UpdateReplacementBoundary.execute(
      expectedPID: 4242,
      expectedBootID: "boot-a",
      expectedLeaseID: "owner-a",
      expectedVersion: "4.2.0",
      persistJournal: { events.append("journal") },
      stopRenewal: { events.append("stop-renewal") },
      seal: {
        events.append("seal")
        return ReplacementSealStatus(
          state: "ready",
          pid: 4242,
          bootID: "boot-a",
          activeTotal: 0,
          leaseID: "owner-a",
          sealed: true,
          version: "4.2.0"
        )
      },
      persistSealedJournal: { events.append("journal-sealed") },
      stopDaemon: { events.append("stop-daemon") }
    )

    XCTAssertEqual(
      events,
      ["journal", "stop-renewal", "seal", "journal-sealed", "stop-daemon"]
    )
  }

  @MainActor
  func testReplacementBoundarySealFailureNeverStopsDaemon() async {
    var events: [String] = []
    do {
      try await UpdateReplacementBoundary.execute(
        expectedPID: 4242,
        expectedBootID: "boot-a",
        expectedLeaseID: "owner-a",
        expectedVersion: "4.2.0",
        persistJournal: { events.append("journal") },
        stopRenewal: { events.append("stop-renewal") },
        seal: {
          events.append("seal")
          throw BoundaryFixtureError.failed
        },
        persistSealedJournal: { events.append("journal-sealed") },
        stopDaemon: { events.append("stop-daemon") }
      )
      XCTFail("seal failure unexpectedly crossed the process-stop boundary")
    } catch is BoundaryFixtureError {
      XCTAssertEqual(events, ["journal", "stop-renewal", "seal"])
    } catch {
      XCTFail("unexpected error: \(error)")
    }
  }

  @MainActor
  func testReplacementBoundaryPIDMismatchNeverStopsDaemon() async {
    var stopped = false
    do {
      try await UpdateReplacementBoundary.execute(
        expectedPID: 4242,
        expectedBootID: "boot-a",
        expectedLeaseID: "owner-a",
        expectedVersion: "4.2.0",
        persistJournal: {},
        stopRenewal: {},
        seal: {
          ReplacementSealStatus(
            state: "ready",
            pid: 9001,
            bootID: "boot-a",
            activeTotal: 0,
            leaseID: "owner-a",
            sealed: true,
            version: "4.2.0"
          )
        },
        persistSealedJournal: {},
        stopDaemon: { stopped = true }
      )
      XCTFail("PID mismatch unexpectedly crossed the process-stop boundary")
    } catch let error as UpdateReplacementBoundaryError {
      XCTAssertEqual(
        error,
        .daemonIdentityChanged(expected: 4242, actual: 9001)
      )
      XCTAssertFalse(stopped)
    } catch {
      XCTFail("unexpected error: \(error)")
    }
  }

  @MainActor
  func testSealedCompletionConfirmsHealthBeforeDelete() async throws {
    var events: [String] = []
    try await UpdateLeaseCompletionBoundary.execute(
      sealed: true,
      expectedPID: 4242,
      expectedBootID: "boot-a",
      expectedLeaseID: "owner-a",
      expectedVersion: "4.2.0",
      stopRenewal: { events.append("stop-renewal") },
      sealLease: {
        XCTFail("an already sealed recovery attempted to seal again")
        throw BoundaryFixtureError.failed
      },
      confirmBootstrap: {
        events.append("confirm")
        return ConfirmedBootstrapStatus(
          pid: 4242,
          bootID: "boot-a",
          leaseID: "owner-a",
          sealed: true,
          bootstrapAuthorized: true,
          version: "4.2.0"
        )
      },
      waitForExactHealth: { events.append("health") },
      cancelLease: { events.append("delete") }
    )

    XCTAssertEqual(events, ["stop-renewal", "confirm", "health", "delete"])
  }

  @MainActor
  func testReplacementBootRaceNeverReachesHealthOrDelete() async {
    var events: [String] = []
    do {
      try await UpdateLeaseCompletionBoundary.execute(
        sealed: true,
        expectedPID: 4242,
        expectedBootID: "boot-a",
        expectedLeaseID: "owner-a",
        expectedVersion: "4.2.0",
        stopRenewal: { events.append("stop-renewal") },
        sealLease: { throw BoundaryFixtureError.failed },
        confirmBootstrap: {
          events.append("confirm")
          return ConfirmedBootstrapStatus(
            pid: 4242,
            bootID: "boot-b",
            leaseID: "owner-a",
            sealed: true,
            bootstrapAuthorized: true,
            version: "4.2.0"
          )
        },
        waitForExactHealth: { events.append("health") },
        cancelLease: { events.append("delete") }
      )
      XCTFail("replacement boot unexpectedly crossed confirmation")
    } catch UpdateLeaseCompletionBoundaryError.invalidConfirmation {
      XCTAssertEqual(events, ["stop-renewal", "confirm"])
    } catch {
      XCTFail("unexpected error: \(error)")
    }
  }

  @MainActor
  func testDeleteAcknowledgementLostRecoveryResealsBeforeConfirmingBootstrap() async throws {
    var events: [String] = []
    try await UpdateLeaseCompletionBoundary.execute(
      sealed: false,
      expectedPID: 4242,
      expectedBootID: "boot-a",
      expectedLeaseID: "owner-a",
      expectedVersion: "4.2.0",
      stopRenewal: { events.append("stop-renewal") },
      sealLease: {
        events.append("seal")
        return ReplacementSealStatus(
          state: "ready",
          pid: 4242,
          bootID: "boot-a",
          activeTotal: 0,
          leaseID: "owner-a",
          sealed: true,
          version: "4.2.0"
        )
      },
      confirmBootstrap: {
        events.append("confirm")
        return ConfirmedBootstrapStatus(
          pid: 4242,
          bootID: "boot-a",
          leaseID: "owner-a",
          sealed: true,
          bootstrapAuthorized: true,
          version: "4.2.0"
        )
      },
      waitForExactHealth: { events.append("health") },
      cancelLease: { events.append("delete") }
    )

    XCTAssertEqual(
      events,
      ["stop-renewal", "seal", "confirm", "health", "delete"]
    )
  }

  @MainActor
  func testHealthFailureRetainsSealedLeaseWithoutDelete() async {
    var events: [String] = []
    do {
      try await UpdateLeaseCompletionBoundary.execute(
        sealed: true,
        expectedPID: 4242,
        expectedBootID: "boot-a",
        expectedLeaseID: "owner-a",
        expectedVersion: "4.2.0",
        stopRenewal: { events.append("stop-renewal") },
        sealLease: { throw BoundaryFixtureError.failed },
        confirmBootstrap: {
          events.append("confirm")
          return ConfirmedBootstrapStatus(
            pid: 4242,
            bootID: "boot-a",
            leaseID: "owner-a",
            sealed: true,
            bootstrapAuthorized: true,
            version: "4.2.0"
          )
        },
        waitForExactHealth: {
          events.append("health")
          throw BoundaryFixtureError.failed
        },
        cancelLease: { events.append("delete") }
      )
      XCTFail("health failure unexpectedly deleted the durable seal")
    } catch is BoundaryFixtureError {
      XCTAssertEqual(events, ["stop-renewal", "confirm", "health"])
    } catch {
      XCTFail("unexpected error: \(error)")
    }
  }

  @MainActor
  func testDeleteBootConflictPropagatesAndCannotCompleteRecovery() async {
    var events: [String] = []
    do {
      try await UpdateLeaseCompletionBoundary.execute(
        sealed: true,
        expectedPID: 4242,
        expectedBootID: "boot-a",
        expectedLeaseID: "owner-a",
        expectedVersion: "4.2.0",
        stopRenewal: { events.append("stop-renewal") },
        sealLease: { throw BoundaryFixtureError.failed },
        confirmBootstrap: {
          events.append("confirm")
          return ConfirmedBootstrapStatus(
            pid: 4242,
            bootID: "boot-a",
            leaseID: "owner-a",
            sealed: true,
            bootstrapAuthorized: true,
            version: "4.2.0"
          )
        },
        waitForExactHealth: { events.append("health") },
        cancelLease: {
          events.append("delete")
          throw BoundaryFixtureError.failed
        }
      )
      XCTFail("a rejected DELETE unexpectedly completed recovery")
    } catch is BoundaryFixtureError {
      XCTAssertEqual(events, ["stop-renewal", "confirm", "health", "delete"])
    } catch {
      XCTFail("unexpected error: \(error)")
    }
  }

  @MainActor
  func testLoadedRecoveryRestoresMissingJobBeforeWaiting() async throws {
    var events: [String] = []
    let status: String = try await UpdateDaemonRestorationBoundary.execute(
      wasLoaded: true,
      wasDisabled: false,
      currentLoadedJobIsValid: {
        events.append("inspect-loaded")
        return false
      },
      restoreLoadedJob: { disabled in
        events.append("restore-loaded:\(disabled)")
      },
      currentDetachedStatus: {
        XCTFail("loaded recovery inspected detached state")
        return nil
      },
      ensureDetachedCanRestart: {
        XCTFail("loaded recovery tried detached restart")
      },
      restartDetached: {
        XCTFail("loaded recovery restarted detached daemon")
      },
      waitForStatus: {
        events.append("wait-status")
        return "ready"
      }
    )

    XCTAssertEqual(status, "ready")
    XCTAssertEqual(events, ["inspect-loaded", "restore-loaded:false", "wait-status"])
  }

  @MainActor
  func testDetachedRecoveryRestartsOnlyAfterAbsenceIsProven() async throws {
    var events: [String] = []
    let status: String = try await UpdateDaemonRestorationBoundary.execute(
      wasLoaded: false,
      wasDisabled: false,
      currentLoadedJobIsValid: {
        XCTFail("detached recovery inspected a loaded job branch")
        return false
      },
      restoreLoadedJob: { _ in
        XCTFail("detached recovery restored a LaunchAgent")
      },
      currentDetachedStatus: {
        events.append("probe-detached")
        return nil
      },
      ensureDetachedCanRestart: { events.append("prove-absent") },
      restartDetached: { events.append("restart-detached") },
      waitForStatus: {
        events.append("wait-status")
        return "ready"
      }
    )

    XCTAssertEqual(status, "ready")
    XCTAssertEqual(
      events,
      ["probe-detached", "prove-absent", "restart-detached", "wait-status"]
    )
  }

  private func pendingJournal(version: String) -> UpdateRecoveryJournal {
    UpdateRecoveryJournal(
      schemaVersion: UpdateRecoveryJournalStore.currentSchemaVersion,
      expectedVersion: version,
      phase: .pendingInstall,
      leaseID: UUID().uuidString,
      daemonPID: nil,
      launchAgentWasLoaded: nil,
      launchAgentWasDisabled: nil
    )
  }

  private func stoppedJournal(
    version: String,
    phase: UpdateRecoveryPhase
  ) -> UpdateRecoveryJournal {
    UpdateRecoveryJournal(
      schemaVersion: UpdateRecoveryJournalStore.currentSchemaVersion,
      expectedVersion: version,
      phase: phase,
      leaseID: UUID().uuidString,
      daemonPID: 4242,
      daemonBootID: "boot-a",
      daemonVersion: version,
      launchAgentWasLoaded: true,
      launchAgentWasDisabled: false
    )
  }
}
