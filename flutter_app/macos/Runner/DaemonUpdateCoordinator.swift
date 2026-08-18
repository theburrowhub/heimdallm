import Cocoa
import Darwin
import FlutterMacOS
import Security
import Sparkle

private final class RunningProcess: @unchecked Sendable {
  let process: Process
  private let lock = NSLock()
  private var escalationScheduled = false

  init(_ process: Process) {
    self.process = process
  }

  func stopAndEscalate() {
    lock.lock()
    if process.isRunning {
      process.terminate()
    }
    guard !escalationScheduled else {
      lock.unlock()
      return
    }
    escalationScheduled = true
    lock.unlock()

    Task.detached(priority: .utility) { [self] in
      try? await Task.sleep(nanoseconds: 1_000_000_000)
      forceStop()
    }
  }

  private func forceStop() {
    lock.lock()
    defer { lock.unlock() }
    guard process.isRunning else { return }
    // isRunning is checked while retaining the exact Process object that
    // launched this child. Escalation guarantees a launchctl that ignores
    // TERM cannot make updater recovery wait forever.
    _ = Darwin.kill(process.processIdentifier, SIGKILL)
  }
}

private final class OneShotVoidContinuation: @unchecked Sendable {
  private let lock = NSLock()
  private var continuation: CheckedContinuation<Void, Error>?
  private var pendingResult: Result<Void, Error>?
  private var completed = false

  func install(_ continuation: CheckedContinuation<Void, Error>) {
    lock.lock()
    if let pendingResult {
      self.pendingResult = nil
      completed = true
      lock.unlock()
      continuation.resume(with: pendingResult)
      return
    }
    guard !completed else {
      lock.unlock()
      continuation.resume(throwing: CancellationError())
      return
    }
    self.continuation = continuation
    lock.unlock()
  }

  func resume(with result: Result<Void, Error>) {
    lock.lock()
    guard !completed else {
      lock.unlock()
      return
    }
    if let continuation {
      self.continuation = nil
      completed = true
      lock.unlock()
      continuation.resume(with: result)
      return
    }
    guard pendingResult == nil else {
      lock.unlock()
      return
    }
    pendingResult = result
    lock.unlock()
  }
}

private final class CancellableDataTask: @unchecked Sendable {
  private let lock = NSLock()
  private var task: URLSessionDataTask?
  private var cancelled = false

  func resume(_ task: URLSessionDataTask) {
    lock.lock()
    self.task = task
    let shouldCancel = cancelled
    lock.unlock()
    task.resume()
    if shouldCancel { task.cancel() }
  }

  func cancel() {
    lock.lock()
    cancelled = true
    let task = task
    lock.unlock()
    task?.cancel()
  }
}

enum UpdateRecoveryPhase: String, Codable {
  case pendingInstall
  case preparing
  case sealed
  case installing
}

struct UpdateRecoveryJournal: Codable, Equatable {
  let schemaVersion: Int
  let expectedVersion: String
  var phase: UpdateRecoveryPhase
  let leaseID: String
  let daemonPID: Int32?
  let daemonBootID: String?
  let daemonVersion: String?
  let launchAgentWasLoaded: Bool?
  let launchAgentWasDisabled: Bool?

  init(
    schemaVersion: Int,
    expectedVersion: String,
    phase: UpdateRecoveryPhase,
    leaseID: String,
    daemonPID: Int32?,
    daemonBootID: String? = nil,
    daemonVersion: String? = nil,
    launchAgentWasLoaded: Bool?,
    launchAgentWasDisabled: Bool?
  ) {
    self.schemaVersion = schemaVersion
    self.expectedVersion = expectedVersion
    self.phase = phase
    self.leaseID = leaseID
    self.daemonPID = daemonPID
    self.daemonBootID = daemonBootID
    self.daemonVersion = daemonVersion
    self.launchAgentWasLoaded = launchAgentWasLoaded
    self.launchAgentWasDisabled = launchAgentWasDisabled
  }

  func returnedToPendingInstall() -> UpdateRecoveryJournal {
    UpdateRecoveryJournal(
      schemaVersion: schemaVersion,
      expectedVersion: expectedVersion,
      phase: .pendingInstall,
      leaseID: leaseID,
      daemonPID: nil,
      daemonBootID: nil,
      daemonVersion: nil,
      launchAgentWasLoaded: nil,
      launchAgentWasDisabled: nil
    )
  }
}

enum UpdateRecoveryJournalStoreError: LocalizedError {
  case invalid(String)
  case filesystem(String)

  var errorDescription: String? {
    switch self {
    case .invalid(let detail):
      return "Invalid update recovery journal: \(detail)"
    case .filesystem(let detail):
      return "Update recovery journal I/O failed: \(detail)"
    }
  }
}

enum UpdateDrainSealStoreError: LocalizedError {
  case invalid(String)
  case filesystem(String)

  var errorDescription: String? {
    switch self {
    case .invalid(let detail):
      return "Invalid daemon update barrier: \(detail)"
    case .filesystem(let detail):
      return "Daemon update barrier I/O failed: \(detail)"
    }
  }
}

private struct UpdateDrainSealMarker: Codable {
  let leaseID: String
  let sealed: Bool?

  enum CodingKeys: String, CodingKey {
    case leaseID = "lease_id"
    case sealed
  }
}

/// Persists the same minimal sealed marker consumed by Go's persistent
/// workgate. Callers must hold the daemon lifecycle lock, proving there is no
/// process that could admit work while an unsealed/expired marker is upgraded.
struct UpdateDrainSealStore {
  let url: URL
  private let maximumMarkerBytes = 4 * 1024

  func ensureSealed(leaseID: String) throws {
    guard UUID(uuidString: leaseID) != nil else {
      throw UpdateDrainSealStoreError.invalid("lease ID is not a UUID")
    }
    if let existingData = try readExistingMarker() {
      do {
        let existing = try JSONDecoder().decode(
          UpdateDrainSealMarker.self,
          from: existingData
        )
        guard existing.leaseID == leaseID else {
          throw UpdateDrainSealStoreError.invalid("the existing marker has a different owner")
        }
      } catch let error as UpdateDrainSealStoreError {
        throw error
      } catch {
        throw UpdateDrainSealStoreError.invalid(error.localizedDescription)
      }
    }

    let directory = url.deletingLastPathComponent()
    let data: Data
    do {
      let encoder = JSONEncoder()
      encoder.outputFormatting = [.sortedKeys]
      data = try encoder.encode(UpdateDrainSealMarker(leaseID: leaseID, sealed: true))
    } catch {
      throw UpdateDrainSealStoreError.invalid(error.localizedDescription)
    }
    let temporary = directory.appendingPathComponent(
      ".\(url.lastPathComponent).\(UUID().uuidString).tmp"
    )
    var descriptor = Darwin.open(
      temporary.path,
      O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC,
      S_IRUSR | S_IWUSR
    )
    guard descriptor >= 0 else { throw filesystemError("could not create temp marker") }
    var shouldRemoveTemporary = true
    defer {
      if descriptor >= 0 { _ = Darwin.close(descriptor) }
      if shouldRemoveTemporary { _ = Darwin.unlink(temporary.path) }
    }
    try data.withUnsafeBytes { rawBuffer in
      guard let base = rawBuffer.baseAddress else { return }
      var written = 0
      while written < rawBuffer.count {
        let count = Darwin.write(descriptor, base.advanced(by: written), rawBuffer.count - written)
        if count < 0 {
          if errno == EINTR { continue }
          throw filesystemError("could not write temp marker")
        }
        written += count
      }
    }
    guard Darwin.fsync(descriptor) == 0 else {
      throw filesystemError("could not sync temp marker")
    }
    guard Darwin.close(descriptor) == 0 else {
      throw filesystemError("could not close temp marker")
    }
    descriptor = -1
    guard Darwin.rename(temporary.path, url.path) == 0 else {
      throw filesystemError("could not commit sealed marker")
    }
    shouldRemoveTemporary = false

    let directoryDescriptor = Darwin.open(directory.path, O_RDONLY | O_CLOEXEC)
    guard directoryDescriptor >= 0 else {
      throw filesystemError("could not open marker directory")
    }
    defer { _ = Darwin.close(directoryDescriptor) }
    guard Darwin.fsync(directoryDescriptor) == 0 else {
      throw filesystemError("could not sync marker directory")
    }
  }

  private func readExistingMarker() throws -> Data? {
    let descriptor = Darwin.open(url.path, O_RDONLY | O_NOFOLLOW | O_CLOEXEC)
    if descriptor < 0 {
      if errno == ENOENT { return nil }
      throw filesystemError("could not open existing marker")
    }
    defer { _ = Darwin.close(descriptor) }

    var info = stat()
    guard Darwin.fstat(descriptor, &info) == 0 else {
      throw filesystemError("could not inspect existing marker")
    }
    guard (info.st_mode & S_IFMT) == S_IFREG, info.st_uid == getuid() else {
      throw UpdateDrainSealStoreError.invalid(
        "the existing marker must be regular and owned by the current user"
      )
    }
    guard (info.st_mode & 0o077) == 0 else {
      throw UpdateDrainSealStoreError.invalid(
        "the existing marker must not be accessible by group or other users"
      )
    }

    var bytes = [UInt8](repeating: 0, count: maximumMarkerBytes + 1)
    let count: Int = try bytes.withUnsafeMutableBytes { rawBuffer in
      guard let base = rawBuffer.baseAddress else { return 0 }
      var total = 0
      while total < rawBuffer.count {
        let result = Darwin.read(
          descriptor,
          base.advanced(by: total),
          rawBuffer.count - total
        )
        if result < 0 {
          if errno == EINTR { continue }
          throw filesystemError("could not read existing marker")
        }
        if result == 0 { break }
        total += result
      }
      return total
    }
    guard count <= maximumMarkerBytes else {
      throw UpdateDrainSealStoreError.invalid("the existing marker is too large")
    }
    return Data(bytes.prefix(count))
  }

  private func filesystemError(_ context: String) -> UpdateDrainSealStoreError {
    .filesystem("\(context): \(String(cString: strerror(errno)))")
  }
}

struct UpdateAbsentDaemonSealBoundary {
  let lockURL: URL
  let markerURL: URL

  func seal(leaseID: String) throws {
    let descriptor = Darwin.open(
      lockURL.path,
      O_RDWR | O_CREAT | O_NOFOLLOW | O_CLOEXEC,
      S_IRUSR | S_IWUSR
    )
    guard descriptor >= 0 else {
      throw UpdateDrainSealStoreError.filesystem("could not open daemon lifecycle lock")
    }
    defer { _ = Darwin.close(descriptor) }

    var info = stat()
    guard Darwin.fstat(descriptor, &info) == 0,
      (info.st_mode & S_IFMT) == S_IFREG,
      info.st_uid == getuid(),
      (info.st_mode & 0o077) == 0
    else {
      throw UpdateDrainSealStoreError.invalid(
        "the daemon lifecycle lock must be a private current-user regular file"
      )
    }
    guard flock(descriptor, LOCK_EX | LOCK_NB) == 0 else {
      throw UpdateDrainSealStoreError.invalid(
        "another daemon owns the lifecycle lock"
      )
    }
    defer { _ = flock(descriptor, LOCK_UN) }

    try UpdateDrainSealStore(url: markerURL).ensureSealed(leaseID: leaseID)
  }
}

/// Small, deterministic persistence boundary used by the updater and native
/// tests. A journal is either the previous complete file or the new complete
/// file; the temp file is synced before rename and the directory afterwards.
struct UpdateRecoveryJournalStore {
  static let currentSchemaVersion = 1

  let url: URL

  func read() throws -> UpdateRecoveryJournal? {
    var fileInfo = stat()
    if Darwin.lstat(url.path, &fileInfo) != 0 {
      if errno == ENOENT { return nil }
      throw filesystemError()
    }
    guard (fileInfo.st_mode & S_IFMT) == S_IFREG, fileInfo.st_uid == getuid() else {
      throw UpdateRecoveryJournalStoreError.invalid(
        "the file must be regular and owned by the current user"
      )
    }
    guard (fileInfo.st_mode & 0o077) == 0 else {
      throw UpdateRecoveryJournalStoreError.invalid(
        "the file must not be accessible by group or other users"
      )
    }

    do {
      let journal = try JSONDecoder().decode(
        UpdateRecoveryJournal.self,
        from: Data(contentsOf: url, options: [.mappedIfSafe])
      )
      try validate(journal)
      return journal
    } catch let error as UpdateRecoveryJournalStoreError {
      throw error
    } catch {
      throw UpdateRecoveryJournalStoreError.invalid(error.localizedDescription)
    }
  }

  func write(_ journal: UpdateRecoveryJournal) throws {
    try validate(journal)
    let directory = url.deletingLastPathComponent()
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.sortedKeys]
    let data = try encoder.encode(journal)
    let temporary = directory.appendingPathComponent(
      ".\(url.lastPathComponent).\(UUID().uuidString).tmp"
    )
    var descriptor = Darwin.open(
      temporary.path,
      O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC,
      S_IRUSR | S_IWUSR
    )
    guard descriptor >= 0 else { throw filesystemError("could not create temp file") }
    var shouldRemoveTemporary = true
    defer {
      if descriptor >= 0 { _ = Darwin.close(descriptor) }
      if shouldRemoveTemporary { _ = Darwin.unlink(temporary.path) }
    }

    try data.withUnsafeBytes { rawBuffer in
      guard let base = rawBuffer.baseAddress else { return }
      var written = 0
      while written < rawBuffer.count {
        let count = Darwin.write(descriptor, base.advanced(by: written), rawBuffer.count - written)
        if count < 0 {
          if errno == EINTR { continue }
          throw filesystemError("could not write temp file")
        }
        written += count
      }
    }
    guard Darwin.fsync(descriptor) == 0 else {
      throw filesystemError("could not sync temp file")
    }
    guard Darwin.close(descriptor) == 0 else {
      throw filesystemError("could not close temp file")
    }
    descriptor = -1
    guard Darwin.rename(temporary.path, url.path) == 0 else {
      throw filesystemError("could not commit journal")
    }
    shouldRemoveTemporary = false
    try syncDirectory(directory)
  }

  func clear() throws {
    if Darwin.unlink(url.path) != 0, errno != ENOENT {
      throw filesystemError("could not remove journal")
    }
    try syncDirectory(url.deletingLastPathComponent())
  }

  func validate(_ journal: UpdateRecoveryJournal) throws {
    guard journal.schemaVersion == Self.currentSchemaVersion else {
      throw UpdateRecoveryJournalStoreError.invalid("unsupported schema version")
    }
    guard !journal.expectedVersion.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
      throw UpdateRecoveryJournalStoreError.invalid("expected version is missing")
    }
    guard UUID(uuidString: journal.leaseID) != nil else {
      throw UpdateRecoveryJournalStoreError.invalid("lease ID is not a UUID")
    }
    if journal.phase == .pendingInstall {
      guard journal.daemonPID == nil,
        journal.daemonBootID == nil,
        journal.daemonVersion == nil,
        journal.launchAgentWasLoaded == nil,
        journal.launchAgentWasDisabled == nil
      else {
        throw UpdateRecoveryJournalStoreError.invalid(
          "pending install unexpectedly contains stopped-daemon metadata"
        )
      }
      return
    }
    guard let pid = journal.daemonPID, pid > 0,
      let bootID = journal.daemonBootID, !bootID.isEmpty,
      let version = journal.daemonVersion, !version.isEmpty,
      journal.launchAgentWasLoaded != nil,
      journal.launchAgentWasDisabled != nil
    else {
      throw UpdateRecoveryJournalStoreError.invalid("stopped-daemon metadata is incomplete")
    }
  }

  private func syncDirectory(_ directory: URL) throws {
    let descriptor = Darwin.open(directory.path, O_RDONLY | O_CLOEXEC)
    guard descriptor >= 0 else { throw filesystemError("could not open parent directory") }
    defer { _ = Darwin.close(descriptor) }
    guard Darwin.fsync(descriptor) == 0 else {
      throw filesystemError("could not sync parent directory")
    }
  }

  private func filesystemError(_ context: String? = nil) -> UpdateRecoveryJournalStoreError {
    let systemDetail = String(cString: strerror(errno))
    if let context { return .filesystem("\(context): \(systemDetail)") }
    return .filesystem(systemDetail)
  }
}

enum LaunchAgentSnapshotError: LocalizedError {
  case incomplete

  var errorDescription: String? {
    "launchctl output omitted canonical path, program, or PID"
  }
}

struct LaunchAgentSnapshot: Equatable {
  let plistPath: String
  let programPath: String
  let pid: Int32

  static func parse(launchctlPrint output: String) throws -> LaunchAgentSnapshot {
    var plistPath: String?
    var programPath: String?
    var pid: Int32?
    for rawLine in output.split(separator: "\n") {
      let line = rawLine.trimmingCharacters(in: .whitespaces)
      if line.hasPrefix("path = ") {
        plistPath = unquote(String(line.dropFirst("path = ".count)))
      } else if line.hasPrefix("program = ") {
        programPath = unquote(String(line.dropFirst("program = ".count)))
      } else if line.hasPrefix("pid = ") {
        pid = Int32(line.dropFirst("pid = ".count))
      }
    }
    guard let plistPath, !plistPath.isEmpty,
      let programPath, !programPath.isEmpty,
      let pid, pid > 0
    else {
      throw LaunchAgentSnapshotError.incomplete
    }
    return LaunchAgentSnapshot(plistPath: plistPath, programPath: programPath, pid: pid)
  }

  private static func unquote(_ value: String) -> String {
    guard value.count >= 2, value.first == "\"", value.last == "\"" else { return value }
    return String(value.dropFirst().dropLast())
  }
}

struct UpdateAPITokenProvider {
  let path: URL?
  let fallback: String?

  func current() -> String? {
    if let path {
      guard let raw = try? String(contentsOf: path, encoding: .utf8) else { return nil }
      let token = raw.trimmingCharacters(in: .whitespacesAndNewlines)
      return token.isEmpty ? nil : token
    }
    let token = fallback?.trimmingCharacters(in: .whitespacesAndNewlines)
    return token?.isEmpty == false ? token : nil
  }
}

struct ReplacementSealStatus: Equatable {
  let state: String
  let pid: Int32
  let bootID: String
  let activeTotal: Int
  let leaseID: String
  let sealed: Bool
  let version: String?
}

enum UpdateReplacementBoundaryError: LocalizedError, Equatable {
  case leaseIdentityChanged(expected: String, actual: String)
  case daemonIdentityChanged(expected: Int32, actual: Int32)
  case daemonBootChanged(expected: String, actual: String)
  case daemonVersionChanged(expected: String, actual: String?)
  case daemonNotSealed

  var errorDescription: String? {
    switch self {
    case .leaseIdentityChanged(let expected, let actual):
      return "The daemon sealed lease \(actual), expected \(expected)."
    case .daemonIdentityChanged(let expected, let actual):
      return "The daemon changed from PID \(expected) to \(actual) before replacement."
    case .daemonBootChanged(let expected, let actual):
      return "The daemon boot identity changed from \(expected) to \(actual) before replacement."
    case .daemonVersionChanged(let expected, let actual):
      return "The daemon reports version \(actual ?? "missing"), expected \(expected)."
    case .daemonNotSealed:
      return "The daemon did not confirm an idle, durable replacement seal."
    }
  }
}

enum UpdateDaemonVersionPolicyError: LocalizedError, Equatable {
  case missingRunningVersion
  case normalVersionMismatch(expected: String, actual: String)
  case deferredDaemonIsNewer(target: String, actual: String)

  var errorDescription: String? {
    switch self {
    case .missingRunningVersion:
      return "The running daemon did not report a version."
    case .normalVersionMismatch(let expected, let actual):
      return "The running daemon reports \(actual), expected \(expected)."
    case .deferredDaemonIsNewer(let target, let actual):
      return "The running daemon \(actual) is newer than replacement target \(target)."
    }
  }
}

enum UpdateDaemonVersionPolicy {
  static func normalRunningVersion(
    statusVersion: String?,
    currentBundleVersion: String
  ) throws -> String {
    guard let statusVersion, !statusVersion.isEmpty else {
      throw UpdateDaemonVersionPolicyError.missingRunningVersion
    }
    guard statusVersion == currentBundleVersion else {
      throw UpdateDaemonVersionPolicyError.normalVersionMismatch(
        expected: currentBundleVersion,
        actual: statusVersion
      )
    }
    return statusVersion
  }

  static func deferredRunningVersion(
    statusVersion: String?,
    replacementTargetVersion: String
  ) throws -> String {
    guard let statusVersion, !statusVersion.isEmpty else {
      throw UpdateDaemonVersionPolicyError.missingRunningVersion
    }
    let order = SUStandardVersionComparator.default.compareVersion(
      statusVersion,
      toVersion: replacementTargetVersion
    )
    guard order != .orderedDescending else {
      throw UpdateDaemonVersionPolicyError.deferredDaemonIsNewer(
        target: replacementTargetVersion,
        actual: statusVersion
      )
    }
    // A pending-install journal can survive app replacement before the old
    // daemon has stopped. Seal must prove this observed old version remained
    // stable; only the post-restart boundary requires the new bundle version.
    return statusVersion
  }
}

enum UpdateDrainDeadlineError: LocalizedError, Equatable {
  case timedOut

  var errorDescription: String? {
    "Active daemon work did not drain before the update deadline."
  }
}

enum UpdateRecoveryDeadlineError: LocalizedError, Equatable {
  case timedOut

  var errorDescription: String? {
    "Daemon update recovery exceeded its global deadline."
  }
}

enum UpdateCallbackDeadlineError: LocalizedError, Equatable {
  case timedOut

  var errorDescription: String? {
    "The Flutter daemon restart callback timed out."
  }
}

@MainActor
enum UpdateCallbackDeadline {
  static func execute(
    timeoutNanoseconds: UInt64,
    invoke: (@escaping (Result<Void, Error>) -> Void) -> Void
  ) async throws {
    let completion = OneShotVoidContinuation()
    try await withTaskCancellationHandler {
      try Task.checkCancellation()
      try await withCheckedThrowingContinuation {
        (continuation: CheckedContinuation<Void, Error>) in
        completion.install(continuation)
        invoke { result in completion.resume(with: result) }
        Task { @MainActor in
          try? await Task.sleep(nanoseconds: timeoutNanoseconds)
          completion.resume(with: .failure(UpdateCallbackDeadlineError.timedOut))
        }
      }
    } onCancel: {
      completion.resume(with: .failure(CancellationError()))
    }
  }
}

enum UpdateInterruptedPreparationBoundaryError: LocalizedError, Equatable {
  case invalidJournal
  case invalidDaemon
  case leaseIdentityChanged(expected: String, actual: String)
  case daemonVersionChanged(expected: String, actual: String?)
  case bootstrapAlreadyAuthorized

  var errorDescription: String? {
    switch self {
    case .invalidJournal:
      return "The interrupted update journal has incomplete daemon identity metadata."
    case .invalidDaemon:
      return "The replacement daemon did not return a valid idle identity."
    case .leaseIdentityChanged(let expected, let actual):
      return "The daemon returned update lease \(actual), expected \(expected)."
    case .daemonVersionChanged(let expected, let actual):
      return "The daemon reports version \(actual ?? "missing"), expected \(expected)."
    case .bootstrapAlreadyAuthorized:
      return "The interrupted preparation unexpectedly found an authorized daemon bootstrap."
    }
  }
}

struct UpdateInterruptedPreparationRebind: Equatable {
  let journal: UpdateRecoveryJournal
  let replacementAlreadyRunning: Bool
}

/// Rebinds a durable `.preparing` journal to a launchd respawn only after the
/// same lease has authenticated a fully drained daemon. The caller separately
/// verifies the process path and LaunchAgent identity before crossing the
/// seal/stop boundary.
enum UpdateInterruptedPreparationBoundary {
  static func reboundJournal(
    _ journal: UpdateRecoveryJournal,
    installedVersion: String,
    daemonPID: Int32,
    daemonBootID: String,
    leaseID: String,
    state: String,
    activeTotal: Int,
    daemonVersion: String?,
    bootstrapAuthorized: Bool
  ) throws -> UpdateInterruptedPreparationRebind {
    guard journal.phase == .preparing || journal.phase == .sealed,
      let originalPID = journal.daemonPID,
      let originalBootID = journal.daemonBootID,
      let expectedVersion = journal.daemonVersion,
      !expectedVersion.isEmpty,
      journal.launchAgentWasLoaded != nil,
      journal.launchAgentWasDisabled != nil
    else {
      throw UpdateInterruptedPreparationBoundaryError.invalidJournal
    }
    guard leaseID == journal.leaseID else {
      throw UpdateInterruptedPreparationBoundaryError.leaseIdentityChanged(
        expected: journal.leaseID,
        actual: leaseID
      )
    }
    guard daemonPID > 0, !daemonBootID.isEmpty, state == "ready", activeTotal == 0 else {
      throw UpdateInterruptedPreparationBoundaryError.invalidDaemon
    }
    let originalProcess = daemonPID == originalPID && daemonBootID == originalBootID
    let replacementAlreadyRunning = !originalProcess
      && daemonVersion == installedVersion
      && installedVersion != expectedVersion
    guard daemonVersion == expectedVersion || replacementAlreadyRunning else {
      throw UpdateInterruptedPreparationBoundaryError.daemonVersionChanged(
        expected: expectedVersion,
        actual: daemonVersion
      )
    }
    guard !bootstrapAuthorized else {
      throw UpdateInterruptedPreparationBoundaryError.bootstrapAlreadyAuthorized
    }

    return UpdateInterruptedPreparationRebind(
      journal: UpdateRecoveryJournal(
        schemaVersion: journal.schemaVersion,
        expectedVersion: journal.expectedVersion,
        phase: journal.phase,
        leaseID: journal.leaseID,
        daemonPID: daemonPID,
        daemonBootID: daemonBootID,
        daemonVersion: daemonVersion,
        launchAgentWasLoaded: journal.launchAgentWasLoaded,
        launchAgentWasDisabled: journal.launchAgentWasDisabled
      ),
      replacementAlreadyRunning: replacementAlreadyRunning
    )
  }
}

@MainActor
enum UpdateDrainDeadline {
  static func execute<Status: Sendable>(
    timeoutNanoseconds: UInt64,
    operation: @escaping @MainActor @Sendable () async throws -> Status
  ) async throws -> Status {
    try await withThrowingTaskGroup(of: Status.self) { group in
      group.addTask { @MainActor in try await operation() }
      group.addTask {
        try await Task.sleep(nanoseconds: timeoutNanoseconds)
        throw UpdateDrainDeadlineError.timedOut
      }
      defer { group.cancelAll() }
      guard let result = try await group.next() else {
        throw UpdateDrainDeadlineError.timedOut
      }
      return result
    }
  }
}

@MainActor
enum UpdateRecoveryDeadline {
  static func execute<Status: Sendable>(
    timeoutNanoseconds: UInt64,
    operation: @escaping @MainActor @Sendable () async throws -> Status
  ) async throws -> Status {
    try await withThrowingTaskGroup(of: Status.self) { group in
      group.addTask { @MainActor in try await operation() }
      group.addTask {
        try await Task.sleep(nanoseconds: timeoutNanoseconds)
        throw UpdateRecoveryDeadlineError.timedOut
      }
      defer { group.cancelAll() }
      guard let result = try await group.next() else {
        throw UpdateRecoveryDeadlineError.timedOut
      }
      return result
    }
  }
}

@MainActor
enum UpdateRecoveryPolling {
  static func untilSuccess<Status: Sendable>(
    delayNanoseconds: UInt64,
    operation: @escaping @MainActor @Sendable () async throws -> Status
  ) async throws -> Status {
    while true {
      do {
        return try await operation()
      } catch is CancellationError {
        throw CancellationError()
      } catch {
        try await Task.sleep(nanoseconds: delayNanoseconds)
      }
    }
  }
}

/// The destructive replacement boundary is deliberately small and injectable:
/// journal fsync must precede stopping renewal, the authenticated durable seal
/// must precede any process stop, and every validation failure is fail-closed.
@MainActor
enum UpdateReplacementBoundary {
  static func execute(
    expectedPID: Int32,
    expectedBootID: String,
    expectedLeaseID: String,
    expectedVersion: String,
    persistJournal: () throws -> Void,
    stopRenewal: () async -> Void,
    seal: () async throws -> ReplacementSealStatus,
    persistSealedJournal: () throws -> Void,
    stopDaemon: () async throws -> Void
  ) async throws {
    try persistJournal()
    await stopRenewal()
    let status = try await seal()
    guard status.leaseID == expectedLeaseID else {
      throw UpdateReplacementBoundaryError.leaseIdentityChanged(
        expected: expectedLeaseID,
        actual: status.leaseID
      )
    }
    guard status.pid == expectedPID else {
      throw UpdateReplacementBoundaryError.daemonIdentityChanged(
        expected: expectedPID,
        actual: status.pid
      )
    }
    guard status.bootID == expectedBootID else {
      throw UpdateReplacementBoundaryError.daemonBootChanged(
        expected: expectedBootID,
        actual: status.bootID
      )
    }
    guard status.version == expectedVersion else {
      throw UpdateReplacementBoundaryError.daemonVersionChanged(
        expected: expectedVersion,
        actual: status.version
      )
    }
    guard status.state == "ready", status.activeTotal == 0, status.sealed else {
      throw UpdateReplacementBoundaryError.daemonNotSealed
    }
    try persistSealedJournal()
    try await stopDaemon()
  }
}

/// Injectable loaded-vs-detached recovery router. The process-specific
/// closures retain all identity checks; this seam makes the branch behavior
/// and fail-closed restart policy deterministic under XCTest.
@MainActor
enum UpdateDaemonRestorationBoundary {
  static func execute<Status>(
    wasLoaded: Bool,
    wasDisabled: Bool,
    currentLoadedJobIsValid: () async throws -> Bool,
    restoreLoadedJob: (Bool) async throws -> Void,
    currentDetachedStatus: () async -> Status?,
    ensureDetachedCanRestart: () throws -> Void,
    restartDetached: () async throws -> Void,
    waitForStatus: () async throws -> Status
  ) async throws -> Status {
    if wasLoaded {
      if !(try await currentLoadedJobIsValid()) {
        try await restoreLoadedJob(wasDisabled)
      }
    } else if let current = await currentDetachedStatus() {
      return current
    } else {
      try ensureDetachedCanRestart()
      try await restartDetached()
    }
    return try await waitForStatus()
  }
}

struct ConfirmedBootstrapStatus: Equatable {
  let pid: Int32
  let bootID: String
  let leaseID: String
  let sealed: Bool
  let bootstrapAuthorized: Bool
  let version: String?
}

enum UpdateLeaseCompletionBoundaryError: LocalizedError, Equatable {
  case invalidConfirmation

  var errorDescription: String? {
    "The daemon did not authenticate the sealed bootstrap confirmation."
  }
}

/// A sealed replacement remains fail-closed through bootstrap. Renewal stops
/// first, confirmation opens only dependency initialization, exact health is
/// observed while admission stays sealed, and DELETE is necessarily last.
@MainActor
enum UpdateLeaseCompletionBoundary {
  static func execute(
    sealed: Bool,
    expectedPID: Int32,
    expectedBootID: String,
    expectedLeaseID: String,
    expectedVersion: String,
    stopRenewal: () async -> Void,
    sealLease: () async throws -> ReplacementSealStatus,
    confirmBootstrap: () async throws -> ConfirmedBootstrapStatus,
    waitForExactHealth: () async throws -> Void,
    cancelLease: () async throws -> Void
  ) async throws {
    await stopRenewal()
    if !sealed {
      let resealed = try await sealLease()
      guard resealed.pid == expectedPID,
        resealed.bootID == expectedBootID,
        resealed.leaseID == expectedLeaseID,
        resealed.version == expectedVersion,
        resealed.state == "ready",
        resealed.activeTotal == 0,
        resealed.sealed
      else {
        throw UpdateLeaseCompletionBoundaryError.invalidConfirmation
      }
    }
    let confirmed = try await confirmBootstrap()
    guard confirmed.pid == expectedPID,
      confirmed.bootID == expectedBootID,
      confirmed.leaseID == expectedLeaseID,
      confirmed.version == expectedVersion,
      confirmed.sealed,
      confirmed.bootstrapAuthorized
    else {
      throw UpdateLeaseCompletionBoundaryError.invalidConfirmation
    }
    try await waitForExactHealth()
    try await cancelLease()
  }
}

/// Serializes Sparkle installation with the daemon's durable update lease.
/// No bundle replacement is allowed until all daemon work has drained, the
/// canonical process has stopped, and its process-lifetime lock is available.
@MainActor
final class DaemonUpdateCoordinator: NSObject, SPUUpdaterDelegate {
  private typealias Phase = UpdateRecoveryPhase
  private typealias RecoveryJournal = UpdateRecoveryJournal

  private struct DrainStatus: Decodable, Sendable {
    let state: String
    let pid: Int32
    let bootID: String
    let activeTotal: Int
    let leaseID: String
    let sealed: Bool
    let version: String?
    let bootstrapAuthorized: Bool

    enum CodingKeys: String, CodingKey {
      case state
      case pid
      case bootID = "boot_id"
      case activeTotal = "active_total"
      case leaseID = "lease_id"
      case sealed
      case version
      case bootstrapAuthorized = "bootstrap_authorized"
    }
  }

  private struct HealthStatus: Decodable {
    let version: String?
  }

  private struct EmptyResponse: Decodable {}

  private struct CommandOutput {
    let status: Int32
    let stdout: String
    let stderr: String

    var details: String {
      let error = stderr.trimmingCharacters(in: .whitespacesAndNewlines)
      if !error.isEmpty { return error }
      let output = stdout.trimmingCharacters(in: .whitespacesAndNewlines)
      return output.isEmpty ? "exit status \(status)" : output
    }
  }

  private enum CoordinatorError: LocalizedError {
    case invalidConfiguration(String)
    case daemonRequest(String)
    case daemonIdentityChanged(Int32, Int32)
    case leaseIdentityChanged(String, String)
    case launchAgentInspection(String)
    case launchAgentRestore(String)
    case daemonDidNotStop(Int32)
    case daemonDidNotRecover(String)
    case journal(String)
    case processTimedOut(String)

    var errorDescription: String? {
      switch self {
      case .invalidConfiguration(let detail):
        return "Invalid updater configuration: \(detail)"
      case .daemonRequest(let detail):
        return "Daemon update preparation failed: \(detail)"
      case .daemonIdentityChanged(let original, let replacement):
        return "The daemon changed from PID \(original) to \(replacement) while draining."
      case .leaseIdentityChanged(let expected, let actual):
        return "The daemon returned update lease \(actual), expected \(expected)."
      case .launchAgentInspection(let detail):
        return "Could not verify the daemon LaunchAgent: \(detail)"
      case .launchAgentRestore(let detail):
        return "Could not restore the daemon LaunchAgent: \(detail)"
      case .daemonDidNotStop(let pid):
        return "Daemon PID \(pid) did not release its process and data lock."
      case .daemonDidNotRecover(let detail):
        return "The daemon could not be restored safely: \(detail)"
      case .journal(let detail):
        return "The update recovery journal is invalid: \(detail)"
      case .processTimedOut(let executable):
        return "The updater command timed out: \(executable)"
      }
    }
  }

  private let launchAgentLabel = "com.heimdallm.daemon"
  private let updateLeaseHeader = "X-Heimdallm-Update-Lease"
  private let expectedBootIDHeader = "X-Heimdallm-Expected-Boot-ID"
  private let journalFilename = "app-update-recovery.json"
  private let drainRenewalDelay: UInt64 = 5_000_000_000
  private let drainTimeout: UInt64 = 600_000_000_000
  private let recoveryTimeout: UInt64 = 60_000_000_000
  private let stopPollDelay: UInt64 = 100_000_000
  private let stopPollAttempts = 150
  private let commandTimeout: TimeInterval = 10

  private lazy var updaterController = SPUStandardUpdaterController(
    startingUpdater: false,
    updaterDelegate: self,
    userDriverDelegate: nil
  )

  private var channel: FlutterMethodChannel?
  private var apiBaseURL: URL?
  private var fallbackAPIToken: String?
  private var apiTokenPath: URL?
  private var dataDirectory: URL?
  private var pendingInstallHandler: (() -> Void)?
  private var pendingVersion: String?
  private var updatePending = false
  private var operationTask: Task<Void, Never>?
  private var renewalTask: Task<Void, Never>?
  private var renewalFailure: Error?
  private var daemonStopVerified = false
  private var generation: UInt64 = 0
  private var preparationInProgress = false
  private var daemonStoppedForUpdate = false
  private var allowTermination = false
  private var appKitTerminationPostponed = false
  private var recoveryComplete = false
  private var recoveredExpectedVersion: String?
  private var recoveryMustCompleteBeforeTermination = false
  private var terminationAllowedAfterRecoveryFailure = false
  private var updaterStarted = false
  private var pendingPersistenceError: Error?
  private let buildTrustAllowsNativeUpdates: Bool
  private var nativeUpdatesEnabled = false
  private var duplicateInstanceTermination = false

  override init() {
    let trustedBuild = Self.defaultNativeUpdatesEnabled()
    buildTrustAllowsNativeUpdates = trustedBuild
    super.init()
    nativeUpdatesEnabled = trustedBuild
    initializeFilesystemConfiguration(
      dataDirectory: Self.defaultDataDirectory(),
      armRecovery: nativeUpdatesEnabled
    )
  }

#if DEBUG
  init(initialDataDirectory: URL) {
    // RunnerTests use an isolated data directory and must exercise recovery
    // without requiring a Developer ID signature. This initializer does not
    // exist in production builds.
    buildTrustAllowsNativeUpdates = true
    super.init()
    nativeUpdatesEnabled = true
    initializeFilesystemConfiguration(dataDirectory: initialDataDirectory, armRecovery: true)
  }
#endif

  var hasPendingUpdate: Bool { updatePending }

  private nonisolated static func defaultDataDirectory() -> URL {
    if let configured = ProcessInfo.processInfo.environment["HEIMDALLM_DATA_DIR"],
      !configured.isEmpty
    {
      return URL(fileURLWithPath: configured, isDirectory: true).standardizedFileURL
    }
    return FileManager.default.homeDirectoryForCurrentUser
      .appendingPathComponent(".local/share/heimdallm", isDirectory: true)
      .standardizedFileURL
  }

  private nonisolated static func defaultNativeUpdatesEnabled() -> Bool {
    var code: SecCode?
    guard SecCodeCopySelf(SecCSFlags(), &code) == errSecSuccess, let code else { return false }
    var staticCode: SecStaticCode?
    guard SecCodeCopyStaticCode(code, SecCSFlags(), &staticCode) == errSecSuccess,
      let staticCode
    else { return false }
    var rawInformation: CFDictionary?
    guard
      SecCodeCopySigningInformation(
        staticCode,
        SecCSFlags(rawValue: kSecCSSigningInformation),
        &rawInformation
      ) == errSecSuccess,
      let information = rawInformation as? [String: Any],
      let certificates = information[kSecCodeInfoCertificates as String] as? [SecCertificate],
      let leaf = certificates.first
    else { return false }
    var rawCommonName: CFString?
    guard SecCertificateCopyCommonName(leaf, &rawCommonName) == errSecSuccess,
      let commonName = rawCommonName as String?,
      commonName.hasPrefix("Developer ID Application:")
    else {
      // Unsigned, ad-hoc, and Apple Development builds never consume the
      // production feed, even if someone runs Flutter in release mode locally.
      return false
    }
    let task = SecTaskCreateFromSelf(nil)
    let allowsJIT = task.flatMap {
      SecTaskCopyValueForEntitlement(
        $0,
        "com.apple.security.cs.allow-jit" as CFString,
        nil
      ) as? Bool
    } ?? false
    // Debug and Profile use DebugProfile.entitlements (allow-jit=true).
    // The notarized Release entitlement set deliberately omits it.
    return !allowsJIT
  }

  nonisolated static func effectiveNativeUpdatesEnabled(
    buildTrustAllows: Bool,
    requested: Bool
  ) -> Bool {
    buildTrustAllows && requested
  }

  nonisolated static func compareInstalledVersion(
    _ installedVersion: String,
    expected expectedVersion: String
  ) -> ComparisonResult {
    SUStandardVersionComparator.default.compareVersion(
      installedVersion,
      toVersion: expectedVersion
    )
  }

  private func initializeFilesystemConfiguration(
    dataDirectory: URL,
    armRecovery: Bool
  ) {
    let directory = dataDirectory.standardizedFileURL
    self.dataDirectory = directory
    apiTokenPath = directory.appendingPathComponent("api_token", isDirectory: false)
    if armRecovery {
      armRecoveryFromJournal()
    }
  }

  private func armRecoveryFromJournal() {
    recoveryComplete = false
    recoveredExpectedVersion = nil
    terminationAllowedAfterRecoveryFailure = false
    do {
      if let journal = try readJournal() {
        markUpdatePendingInMemory(expectedVersion: journal.expectedVersion)
        pendingPersistenceError = nil
        recoveryMustCompleteBeforeTermination = true
      } else {
        updatePending = false
        pendingVersion = nil
        pendingPersistenceError = nil
        recoveryMustCompleteBeforeTermination = false
      }
    } catch {
      // Corruption or unsafe ownership must also block an early Quit. Recovery
      // will surface the precise error once Flutter requests pending state.
      updatePending = true
      pendingVersion = nil
      pendingPersistenceError = error
      recoveryMustCompleteBeforeTermination = true
    }
  }

  func attach(to flutterViewController: FlutterViewController) {
    let channel = FlutterMethodChannel(
      name: "com.theburrowhub.heimdallm/app_updater",
      binaryMessenger: flutterViewController.engine.binaryMessenger
    )
    self.channel = channel
    channel.setMethodCallHandler { [weak self] call, result in
      guard let self else {
        result(
          FlutterError(
            code: "updater_unavailable",
            message: "The native updater is unavailable.",
            details: nil
          )
        )
        return
      }
      self.handle(call, result: result)
    }
  }

  func applicationShouldTerminate() -> NSApplication.TerminateReply {
    if duplicateInstanceTermination {
      return .terminateNow
    }
    if terminationAllowedAfterRecoveryFailure {
      return .terminateNow
    }
    // A journal discovered before Flutter attaches represents an update whose
    // exact daemon/app state has not been recovered yet. Deny an early Quit;
    // pendingUpdateVersion will either complete recovery or hand the old
    // bundle back to Sparkle's normal drain path.
    if recoveryMustCompleteBeforeTermination {
      return .terminateCancel
    }
    if allowTermination || (!updatePending && !preparationInProgress) {
      return .terminateNow
    }

    appKitTerminationPostponed = true
    if !preparationInProgress {
      startPreparation()
    }
    return .terminateLater
  }

  // MARK: - Flutter bridge

  private func handle(_ call: FlutterMethodCall, result: @escaping FlutterResult) {
    switch call.method {
    case "configure":
      do {
        guard
          let arguments = call.arguments as? [String: Any],
          let base = arguments["apiBaseUrl"] as? String,
          let baseURL = URL(string: base),
          let host = baseURL.host?.lowercased(),
          baseURL.scheme == "http",
          ["127.0.0.1", "localhost", "::1"].contains(host),
          let dataDir = arguments["dataDir"] as? String,
          !dataDir.isEmpty
        else {
          throw CoordinatorError.invalidConfiguration(
            "the daemon endpoint must be loopback HTTP and the data directory is required"
          )
        }
        apiBaseURL = baseURL
        let updatesRequested = arguments["updatesEnabled"] as? Bool ?? false
        nativeUpdatesEnabled = Self.effectiveNativeUpdatesEnabled(
          buildTrustAllows: buildTrustAllowsNativeUpdates,
          requested: updatesRequested
        )
        fallbackAPIToken = (arguments["apiToken"] as? String)?.trimmingCharacters(
          in: .whitespacesAndNewlines
        )
        if let tokenPath = arguments["apiTokenPath"] as? String, !tokenPath.isEmpty {
          apiTokenPath = URL(fileURLWithPath: tokenPath, isDirectory: false).standardizedFileURL
        } else {
          // Inline tokens exist only for deterministic bridge tests and
          // backwards compatibility. Production supplies a path so a token
          // created or rotated after configure is observed immediately.
          apiTokenPath = nil
        }
        dataDirectory = URL(fileURLWithPath: dataDir, isDirectory: true).standardizedFileURL
        try FileManager.default.createDirectory(
          at: dataDirectory!,
          withIntermediateDirectories: true,
          attributes: [.posixPermissions: 0o700]
        )
        if nativeUpdatesEnabled {
          armRecoveryFromJournal()
        } else {
          resetAfterCompletedUpdateState()
        }
        // Dart must expose the updater only when the native signature gate also
        // accepted this build. A release-mode ad-hoc build therefore remains
        // fail-closed in both layers.
        result(nativeUpdatesEnabled)
      } catch {
        result(flutterError(error))
      }

    case "checkForUpdates":
      guard nativeUpdatesEnabled else {
        result(
          FlutterError(
            code: "updater_unavailable",
            message: "Native updates are disabled for this build.",
            details: nil
          )
        )
        return
      }
      guard updaterStarted else {
        result(
          FlutterError(
            code: "updater_not_ready",
            message: "Update recovery has not completed yet.",
            details: nil
          )
        )
        return
      }
      guard updaterController.updater.canCheckForUpdates else {
        result(
          FlutterError(
            code: "update_check_busy",
            message: "An update check is already in progress.",
            details: nil
          )
        )
        return
      }
      updaterController.checkForUpdates(nil)
      result(nil)

    case "pendingUpdateVersion":
      guard nativeUpdatesEnabled else {
        result(nil)
        return
      }
      terminationAllowedAfterRecoveryFailure = false
      Task { @MainActor in
        do {
          let expectedVersion = try await recoverInterruptedUpdateIfNeeded()
          startUpdaterIfNeeded()
          result(expectedVersion)
        } catch {
          await stopLeaseRenewal()
          allowTerminationAfterRecoveryFailure()
          result(flutterError(error))
        }
      }

    case "completeUpdate":
      guard nativeUpdatesEnabled else {
        result(nil)
        return
      }
      terminationAllowedAfterRecoveryFailure = false
      Task { @MainActor in
        do {
          try await completeRecoveredUpdate()
          result(nil)
        } catch {
          await stopLeaseRenewal()
          allowTerminationAfterRecoveryFailure()
          result(flutterError(error))
        }
      }

    case "terminateApplication":
      result(nil)
      NSApp.terminate(nil)

    case "terminateDuplicateApplication":
      allowDuplicateInstanceTermination()
      result(nil)
      NSApp.terminate(nil)

    default:
      result(FlutterMethodNotImplemented)
    }
  }

  // MARK: - Sparkle lifecycle

  func updater(_ updater: SPUUpdater, didExtractUpdate item: SUAppcastItem) {
    recordPendingInstall(item)
  }

  func updater(_ updater: SPUUpdater, willInstallUpdate item: SUAppcastItem) {
    recordPendingInstall(item)
  }

  func updater(
    _ updater: SPUUpdater,
    shouldPostponeRelaunchForUpdate item: SUAppcastItem,
    untilInvokingBlock installHandler: @escaping () -> Void
  ) -> Bool {
    recordPendingInstall(item)
    pendingInstallHandler = installHandler
    startPreparation()
    return true
  }

  func updater(
    _ updater: SPUUpdater,
    willInstallUpdateOnQuit item: SUAppcastItem,
    immediateInstallationBlock immediateInstallHandler: @escaping () -> Void
  ) -> Bool {
    recordPendingInstall(item)
    pendingInstallHandler = immediateInstallHandler
    return true
  }

  func updaterShouldRelaunchApplication(_ updater: SPUUpdater) -> Bool {
    true
  }

  func updater(_ updater: SPUUpdater, didAbortWithError error: Error) {
    guard updatePending || preparationInProgress || daemonStoppedForUpdate else { return }
    abortPreparation(reason: error.localizedDescription)
  }

  private func startUpdaterIfNeeded() {
    guard nativeUpdatesEnabled, !updaterStarted else { return }
    updaterController.startUpdater()
    updaterStarted = true
  }

  func allowDuplicateInstanceTermination() {
    duplicateInstanceTermination = true
  }

  func allowTerminationAfterRecoveryFailure() {
    // The durable journal remains authoritative for the next launch. Once a
    // recovery attempt has returned an error, keeping the process impossible
    // to quit provides no additional safety and prevents the UI's documented
    // quit/reopen remediation.
    terminationAllowedAfterRecoveryFailure = true
  }

  private func recordPendingInstall(_ item: SUAppcastItem) {
    markUpdatePendingInMemory(expectedVersion: item.displayVersionString)
    do {
      _ = try ensurePendingJournal(expectedVersion: item.displayVersionString)
      pendingPersistenceError = nil
    } catch {
      pendingPersistenceError = error
      NSLog("Heimdallm updater could not persist pending installation: \(error)")
    }
  }

  func markUpdatePendingInMemory(expectedVersion: String) {
    updatePending = true
    pendingVersion = expectedVersion
  }

  private func ensurePendingJournal(expectedVersion: String) throws -> RecoveryJournal {
    if let existing = try readJournal() {
      guard existing.expectedVersion == expectedVersion else {
        throw CoordinatorError.journal(
          "pending update \(existing.expectedVersion) conflicts with \(expectedVersion)"
        )
      }
      return existing
    }
    let journal = RecoveryJournal(
      schemaVersion: 1,
      expectedVersion: expectedVersion,
      phase: .pendingInstall,
      leaseID: UUID().uuidString.lowercased(),
      daemonPID: nil,
      launchAgentWasLoaded: nil,
      launchAgentWasDisabled: nil
    )
    try writeJournal(journal)
    return journal
  }

  private func startPreparation() {
    guard !preparationInProgress else { return }
    if let pendingPersistenceError {
      denyPostponedTermination()
      showPreparationError(pendingPersistenceError.localizedDescription)
      return
    }
    guard updatePending, let expectedVersion = pendingVersion, !expectedVersion.isEmpty else {
      denyPostponedTermination()
      return
    }

    let pendingJournal: RecoveryJournal
    do {
      pendingJournal = try ensurePendingJournal(expectedVersion: expectedVersion)
    } catch {
      denyPostponedTermination()
      showPreparationError(error.localizedDescription)
      return
    }

    generation &+= 1
    let operationGeneration = generation
    let leaseID = pendingJournal.leaseID
    preparationInProgress = true
    daemonStopVerified = false
    renewalFailure = nil

    operationTask = Task { @MainActor [weak self] in
      guard let self else { return }
      do {
        let status = try await drainDaemonWork(leaseID: leaseID)
        try ensureCurrent(operationGeneration)
        startLeaseRenewal(
          leaseID: leaseID,
          daemonPID: status.pid,
          daemonBootID: status.bootID
        )

        let launchAgent = try await inspectLaunchAgent()
        try ensureCurrent(operationGeneration)
        if let launchAgent {
          try verifyLaunchAgent(launchAgent, daemonPID: status.pid)
        } else {
          try verifyProcessExecutable(status.pid)
        }
        let serviceDisabled = try await launchAgentIsDisabled()
        try ensureCurrent(operationGeneration)
        let runningVersion = try UpdateDaemonVersionPolicy.normalRunningVersion(
          statusVersion: status.version,
          currentBundleVersion: currentBundleVersion()
        )

        var journal = RecoveryJournal(
          schemaVersion: 1,
          expectedVersion: expectedVersion,
          phase: .preparing,
          leaseID: leaseID,
          daemonPID: status.pid,
          daemonBootID: status.bootID,
          daemonVersion: runningVersion,
          launchAgentWasLoaded: launchAgent != nil,
          launchAgentWasDisabled: serviceDisabled
        )
        try await persistSealAndStopDaemon(
          journal: journal,
          status: status,
          launchAgent: launchAgent,
          expectedRunningVersion: runningVersion
        )
        try ensureCurrent(operationGeneration)
        daemonStoppedForUpdate = true
        daemonStopVerified = true
        await stopLeaseRenewal()

        journal.phase = .installing
        try writeJournal(journal)
        allowTermination = true
        preparationInProgress = false
        operationTask = nil

        if let installHandler = pendingInstallHandler {
          pendingInstallHandler = nil
          installHandler()
        }
        replyToPostponedTermination(true)
      } catch is CancellationError {
        // The replacement operation waits for this Task to finish before it
        // observes state and performs rollback.
      } catch {
        await stopLeaseRenewal()
        await rollbackAfterStoppedOperation(
          leaseID: leaseID,
          reason: error.localizedDescription,
          clearSparkleState: false,
          generation: operationGeneration
        )
      }
    }
  }

  private func abortPreparation(reason: String) {
    generation &+= 1
    let abortGeneration = generation
    let previous = operationTask
    previous?.cancel()
    operationTask = Task { @MainActor [weak self] in
      _ = await previous?.value
      guard let self, self.generation == abortGeneration else { return }
      await self.stopLeaseRenewal()
      let journal = try? self.readJournal()
      let leaseID = journal?.leaseID
      await self.rollbackAfterStoppedOperation(
        leaseID: leaseID,
        reason: reason,
        clearSparkleState: true,
        generation: abortGeneration
      )
    }
  }

  private func rollbackAfterStoppedOperation(
    leaseID: String?,
    reason: String,
    clearSparkleState: Bool,
    generation operationGeneration: UInt64
  ) async {
    guard generation == operationGeneration else { return }
    await stopLeaseRenewal()
    var recoveryFailure: Error?

    do {
      if let journal = try readJournal() {
        var restored: DrainStatus?
        var installedVersion: String?
        if journal.phase != .pendingInstall {
          let version = try currentBundleVersion()
          installedVersion = version
          restored = try await restoreAndVerifyDaemon(
            journal,
            expectedVersion: version
          )
        }
        if let restored, let installedVersion {
          try await releaseLeaseAndVerifyDaemon(
            journal,
            status: restored,
            expectedVersion: installedVersion
          )
        } else {
          try await cancelUnsealedUpdateLease(journal.leaseID)
        }
        if clearSparkleState {
          try clearJournal()
        } else {
          try writeJournal(
            RecoveryJournal(
              schemaVersion: 1,
              expectedVersion: journal.expectedVersion,
              phase: .pendingInstall,
              leaseID: journal.leaseID,
              daemonPID: nil,
              launchAgentWasLoaded: nil,
              launchAgentWasDisabled: nil
            )
          )
        }
      } else if let leaseID {
        try await cancelUnsealedUpdateLease(leaseID)
      }
    } catch {
      recoveryFailure = error
    }

    resetInMemoryPreparation(clearSparkleState: clearSparkleState)
    if let recoveryFailure {
      showPreparationError(
        "\(reason)\n\nRecovery could not be verified. "
          + "The journal was retained for the next launch.\n\n"
          + recoveryFailure.localizedDescription
      )
    } else {
      showPreparationError(reason)
    }
  }

  private func resetInMemoryPreparation(clearSparkleState: Bool) {
    preparationInProgress = false
    daemonStoppedForUpdate = false
    daemonStopVerified = false
    allowTermination = false
    operationTask = nil
    renewalTask = nil
    renewalFailure = nil
    if clearSparkleState {
      updatePending = false
      pendingInstallHandler = nil
      pendingVersion = nil
    }
    denyPostponedTermination()
  }

  private func ensureCurrent(_ operationGeneration: UInt64) throws {
    try Task.checkCancellation()
    guard generation == operationGeneration else { throw CancellationError() }
    if let renewalFailure { throw renewalFailure }
  }

  private func replyToPostponedTermination(_ shouldTerminate: Bool) {
    guard appKitTerminationPostponed else { return }
    appKitTerminationPostponed = false
    NSApp.reply(toApplicationShouldTerminate: shouldTerminate)
  }

  private func denyPostponedTermination() {
    replyToPostponedTermination(false)
  }

  private func showPreparationError(_ reason: String) {
    let alert = NSAlert()
    alert.alertStyle = .critical
    alert.messageText = "Heimdallm could not install the update safely"
    alert.informativeText =
      "No files were replaced. Heimdallm will only continue when the daemon state is verified.\n\n\(reason)"
    alert.addButton(withTitle: "OK")
    alert.runModal()
  }

  // MARK: - Recovery across app replacement

  private func recoverInterruptedUpdateIfNeeded() async throws -> String? {
    if recoveryComplete { return recoveredExpectedVersion }
    guard let journal = try readJournal() else {
      recoveryComplete = true
      recoveredExpectedVersion = nil
      recoveryMustCompleteBeforeTermination = false
      return nil
    }

    let installedVersion = try currentBundleVersion()
    let installedVersionOrder = Self.compareInstalledVersion(
      installedVersion,
      expected: journal.expectedVersion
    )
    updatePending = true
    pendingVersion = journal.expectedVersion

    if installedVersionOrder == .orderedDescending {
      try await discardJournalSupersededByNewerBundle(
        journal,
        installedVersion: installedVersion
      )
      return nil
    }

    if journal.phase == .pendingInstall {
      if installedVersionOrder == .orderedSame {
        let completed = try await finishDeferredDaemonTransition(journal)
        startLeaseRenewal(
          leaseID: completed.journal.leaseID,
          daemonPID: completed.status.pid,
          daemonBootID: completed.status.bootID
        )
        recoveryComplete = true
        recoveredExpectedVersion = completed.journal.expectedVersion
        recoveryMustCompleteBeforeTermination = true
        return completed.journal.expectedVersion
      }
      // Sparkle may still own an install-on-quit transaction in the old app.
      // Keep the journal so every ordinary termination must pass through drain.
      recoveryComplete = true
      recoveredExpectedVersion = nil
      recoveryMustCompleteBeforeTermination = false
      return nil
    }

    let restored = try await restoreAndVerifyDaemon(
      journal,
      expectedVersion: installedVersion
    )
    if installedVersionOrder == .orderedAscending {
      // Do not discard a resumable Sparkle installation merely because the old
      // bundle relaunched. Release the gate so ordinary work resumes, but keep
      // a canonical pending journal so the next Quit reacquires and drains.
      try await releaseLeaseAndVerifyDaemon(
        journal,
        status: restored,
        expectedVersion: installedVersion
      )
      try writeJournal(journal.returnedToPendingInstall())
      recoveryComplete = true
      recoveredExpectedVersion = nil
      recoveryMustCompleteBeforeTermination = false
      return nil
    }

    startLeaseRenewal(
      leaseID: journal.leaseID,
      daemonPID: restored.pid,
      daemonBootID: restored.bootID
    )

    // Dart performs one more exact /health.version comparison before entering
    // the UI. The durable lease and journal remain until completeUpdate.
    recoveryComplete = true
    recoveredExpectedVersion = journal.expectedVersion
    recoveryMustCompleteBeforeTermination = true
    return journal.expectedVersion
  }

  private func discardJournalSupersededByNewerBundle(
    _ journal: RecoveryJournal,
    installedVersion: String
  ) async throws {
    // A manually installed/newer signed app must never keep trying to install
    // or re-create a journal for an older Sparkle item. Restore a daemon that
    // was already stopped, authenticate an idempotent lease cancellation, and
    // only then remove the stale journal.
    let restored: DrainStatus
    let recoveryJournal: RecoveryJournal
    if journal.phase == .pendingInstall {
      // A newer app can coexist briefly with the older daemon process left by
      // a manual replacement. Run the same durable drain/stop/restore
      // transition, but target the actually installed newer bundle.
      let completed = try await finishDeferredDaemonTransition(
        journal,
        expectedVersion: installedVersion
      )
      recoveryJournal = completed.journal
      restored = completed.status
    } else {
      recoveryJournal = journal
      restored = try await restoreAndVerifyDaemon(
        journal,
        expectedVersion: installedVersion
      )
    }
    try await releaseLeaseAndVerifyDaemon(
      recoveryJournal,
      status: restored,
      expectedVersion: installedVersion
    )
    try clearJournal()
    resetAfterCompletedUpdateState()
  }

  private func completeRecoveredUpdate() async throws {
    guard let journal = try readJournal() else {
      resetAfterCompletedUpdateState()
      return
    }
    let installedVersion = try currentBundleVersion()
    guard installedVersion == journal.expectedVersion else {
      throw CoordinatorError.journal(
        "installed app \(installedVersion) does not match expected \(journal.expectedVersion)"
      )
    }
    let restored = try await restoreAndVerifyDaemon(
      journal,
      expectedVersion: installedVersion
    )
    try await releaseLeaseAndVerifyDaemon(
      journal,
      status: restored,
      expectedVersion: installedVersion
    )
    try clearJournal()
    resetAfterCompletedUpdateState()
  }

  func resetAfterCompletedUpdateState() {
    generation &+= 1
    updatePending = false
    pendingVersion = nil
    pendingInstallHandler = nil
    pendingPersistenceError = nil
    preparationInProgress = false
    daemonStoppedForUpdate = false
    daemonStopVerified = false
    allowTermination = false
    operationTask = nil
    renewalTask = nil
    renewalFailure = nil
    recoveryComplete = true
    recoveredExpectedVersion = nil
    recoveryMustCompleteBeforeTermination = false
    terminationAllowedAfterRecoveryFailure = false
  }

  private func restoreAndVerifyDaemon(
    _ journal: RecoveryJournal,
    expectedVersion: String
  ) async throws -> DrainStatus {
    if journal.phase == .preparing || journal.phase == .sealed {
      let stopped = try await finishInterruptedPreparation(
        journal,
        installedVersion: expectedVersion
      )
      return try await restoreAndVerifyDaemon(
        stopped,
        expectedVersion: expectedVersion
      )
    }
    let metadata = try stoppedMetadata(journal)
    return try await UpdateDaemonRestorationBoundary.execute(
      wasLoaded: metadata.wasLoaded,
      wasDisabled: metadata.wasDisabled,
      currentLoadedJobIsValid: {
        guard let current = try await self.inspectLaunchAgent() else { return false }
        try self.verifyLaunchAgent(current, daemonPID: current.pid)
        return true
      },
      restoreLoadedJob: { disabled in
        try await self.restoreLaunchAgent(disabled: disabled)
      },
      currentDetachedStatus: {
        await self.daemonStatusIfVerifiableSoon(
          journal,
          expectedVersion: expectedVersion
        )
      },
      ensureDetachedCanRestart: {
        guard !Self.processIsAlive(metadata.pid), try self.daemonLockIsAvailable() else {
          throw CoordinatorError.daemonDidNotRecover(
            "the previous detached daemon has not released its identity lock"
          )
        }
      },
      restartDetached: { try await self.requestFlutterDaemonRestart() },
      waitForStatus: {
        try await self.waitForExpectedDaemon(
          journal,
          expectedVersion: expectedVersion
        )
      }
    )
  }

  private func finishInterruptedPreparation(
    _ journal: RecoveryJournal,
    installedVersion: String
  ) async throws -> RecoveryJournal {
    let metadata = try stoppedMetadata(journal)
    guard journal.daemonBootID != nil, journal.daemonVersion != nil else {
      throw CoordinatorError.journal(
        "interrupted preparation lacks daemon boot/version identity"
      )
    }
    var resumableJournal = journal

    var launchAgent = try await inspectLaunchAgent()
    if !metadata.wasLoaded, launchAgent != nil {
      throw CoordinatorError.launchAgentInspection(
        "a LaunchAgent appeared for a previously detached daemon"
      )
    }

    // Only the post-seal journal can prove that a missing daemon was stopped
    // behind the durable work barrier. A `.preparing` journal alone never
    // crosses this branch because the process may have died before Seal.
    if launchAgent == nil,
      !Self.processIsAlive(metadata.pid),
      try daemonLockIsAvailable(),
      resumableJournal.phase == .sealed
    {
      var stopped = resumableJournal
      stopped.phase = .installing
      try writeJournal(stopped)
      return stopped
    }

    var restartedMissingDaemon = false
    if launchAgent == nil,
      !Self.processIsAlive(metadata.pid),
      try daemonLockIsAvailable(),
      resumableJournal.phase == .preparing
    {
      // Seal was never acknowledged by a live daemon. While the daemon lock is
      // exclusively held, commit the same minimal persistent sealed marker Go
      // restores before any stateful bootstrap. Only then may a bundled daemon
      // be started and asked to authenticate that owner through HTTP.
      try sealUpdateBarrierWhileDaemonAbsent(leaseID: resumableJournal.leaseID)
      resumableJournal.phase = .sealed
      try writeJournal(resumableJournal)
      if metadata.wasLoaded {
        try await restoreLaunchAgent(disabled: metadata.wasDisabled)
      } else {
        try await requestFlutterDaemonRestart()
      }
      restartedMissingDaemon = true
    }

    // If KeepAlive respawned the job after the original process died, the
    // persistent gate restores the same lease. Reauthenticate that owner and
    // bind the journal to the new PID/boot before sealing and booting it out.
    let status = restartedMissingDaemon
      ? try await waitForRestartedDaemonDrain(leaseID: resumableJournal.leaseID)
      : try await drainDaemonWork(leaseID: resumableJournal.leaseID)
    if status.pid != metadata.pid, Self.processIsAlive(metadata.pid) {
      throw CoordinatorError.daemonIdentityChanged(metadata.pid, status.pid)
    }
    launchAgent = try await inspectLaunchAgent()
    if !metadata.wasLoaded, launchAgent != nil {
      throw CoordinatorError.launchAgentInspection(
        "a LaunchAgent appeared for a previously detached daemon"
      )
    }
    if let launchAgent {
      try verifyLaunchAgent(launchAgent, daemonPID: status.pid)
    } else {
      try verifyProcessExecutable(status.pid)
    }

    let rebind = try UpdateInterruptedPreparationBoundary.reboundJournal(
      resumableJournal,
      installedVersion: installedVersion,
      daemonPID: status.pid,
      daemonBootID: status.bootID,
      leaseID: status.leaseID,
      state: status.state,
      activeTotal: status.activeTotal,
      daemonVersion: status.version,
      bootstrapAuthorized: status.bootstrapAuthorized
    )
    let rebound = rebind.journal
    guard let expectedRunningVersion = rebound.daemonVersion else {
      throw CoordinatorError.journal("interrupted preparation lost daemon version identity")
    }
    startLeaseRenewal(
      leaseID: rebound.leaseID,
      daemonPID: status.pid,
      daemonBootID: status.bootID
    )
    do {
      try await persistSealAndStopDaemon(
        journal: rebound,
        status: status,
        launchAgent: launchAgent,
        expectedRunningVersion: expectedRunningVersion,
        shouldStopDaemon: !rebind.replacementAlreadyRunning
      )
      if !rebind.replacementAlreadyRunning {
        daemonStoppedForUpdate = true
        daemonStopVerified = true
      }
    } catch {
      await stopLeaseRenewal()
      throw error
    }

    await stopLeaseRenewal()
    var stopped = rebound
    stopped.phase = .installing
    try writeJournal(stopped)
    return stopped
  }

  private func waitForRestartedDaemonDrain(leaseID: String) async throws -> DrainStatus {
    do {
      return try await UpdateRecoveryDeadline.execute(
        timeoutNanoseconds: recoveryTimeout
      ) { [self] in
        try await UpdateRecoveryPolling.untilSuccess(
          delayNanoseconds: stopPollDelay
        ) {
          _ = try await prepareDaemon(leaseID: leaseID)
          return try await drainDaemonWork(leaseID: leaseID)
        }
      }
    } catch UpdateRecoveryDeadlineError.timedOut {
      throw CoordinatorError.daemonDidNotRecover(
        "the restarted daemon did not become drainable before the recovery deadline"
      )
    }
  }

  private func stoppedMetadata(
    _ journal: RecoveryJournal
  ) throws -> (pid: Int32, wasLoaded: Bool, wasDisabled: Bool) {
    guard
      journal.phase != .pendingInstall,
      let pid = journal.daemonPID,
      pid > 0,
      let wasLoaded = journal.launchAgentWasLoaded,
      let wasDisabled = journal.launchAgentWasDisabled
    else {
      throw CoordinatorError.journal("daemon stop metadata is incomplete")
    }
    return (pid, wasLoaded, wasDisabled)
  }

  private func finishDeferredDaemonTransition(
    _ pendingJournal: RecoveryJournal,
    expectedVersion: String? = nil
  ) async throws -> (journal: RecoveryJournal, status: DrainStatus) {
    let targetVersion = expectedVersion ?? pendingJournal.expectedVersion
    let status = try await drainDaemonWork(leaseID: pendingJournal.leaseID)
    startLeaseRenewal(
      leaseID: pendingJournal.leaseID,
      daemonPID: status.pid,
      daemonBootID: status.bootID
    )
    let launchAgent = try await inspectLaunchAgent()
    if let launchAgent {
      try verifyLaunchAgent(launchAgent, daemonPID: status.pid)
    } else {
      try verifyProcessExecutable(status.pid)
    }
    let disabled = try await launchAgentIsDisabled()
    let runningVersion = try UpdateDaemonVersionPolicy.deferredRunningVersion(
      statusVersion: status.version,
      replacementTargetVersion: targetVersion
    )
    var journal = RecoveryJournal(
      schemaVersion: 1,
      expectedVersion: targetVersion,
      phase: .preparing,
      leaseID: pendingJournal.leaseID,
      daemonPID: status.pid,
      daemonBootID: status.bootID,
      daemonVersion: runningVersion,
      launchAgentWasLoaded: launchAgent != nil,
      launchAgentWasDisabled: disabled
    )
    try await persistSealAndStopDaemon(
      journal: journal,
      status: status,
      launchAgent: launchAgent,
      expectedRunningVersion: runningVersion
    )
    daemonStopVerified = true
    await stopLeaseRenewal()
    journal.phase = .installing
    try writeJournal(journal)
    let restored = try await restoreAndVerifyDaemon(
      journal,
      expectedVersion: targetVersion
    )
    return (journal, restored)
  }

  private func daemonStatusIfVerifiableSoon(
    _ journal: RecoveryJournal,
    expectedVersion: String
  ) async -> DrainStatus? {
    do {
      return try await verifyExpectedDaemon(journal, expectedVersion: expectedVersion)
    } catch {
      return nil
    }
  }

  private func waitForExpectedDaemon(
    _ journal: RecoveryJournal,
    expectedVersion: String
  ) async throws -> DrainStatus {
    do {
      return try await UpdateRecoveryDeadline.execute(
        timeoutNanoseconds: recoveryTimeout
      ) { [self] in
        try await UpdateRecoveryPolling.untilSuccess(
          delayNanoseconds: stopPollDelay
        ) {
          try await verifyExpectedDaemon(journal, expectedVersion: expectedVersion)
        }
      }
    } catch UpdateRecoveryDeadlineError.timedOut {
      throw CoordinatorError.daemonDidNotRecover(
        "the expected daemon did not appear before the recovery deadline"
      )
    }
  }

  private func verifyExpectedDaemon(
    _ journal: RecoveryJournal,
    expectedVersion: String
  ) async throws -> DrainStatus {
    let status = try await prepareDaemon(leaseID: journal.leaseID)
    guard status.state == "ready", status.activeTotal == 0 else {
      throw CoordinatorError.daemonDidNotRecover(
        "the restored daemon did not remain drained"
      )
    }
    guard status.version == expectedVersion else {
      throw CoordinatorError.daemonDidNotRecover(
        "daemon version \(status.version ?? "missing") does not match \(expectedVersion)"
      )
    }
    let metadata = try stoppedMetadata(journal)
    if metadata.wasLoaded {
      guard let launchAgent = try await inspectLaunchAgent() else {
        throw CoordinatorError.launchAgentRestore("the restored job is not loaded")
      }
      try verifyLaunchAgent(launchAgent, daemonPID: status.pid)
    } else {
      guard try await inspectLaunchAgent() == nil else {
        throw CoordinatorError.daemonDidNotRecover(
          "a LaunchAgent was loaded for a previously detached daemon"
        )
      }
      try verifyProcessExecutable(status.pid)
    }

    return status
  }

  private func releaseLeaseAndVerifyDaemon(
    _ journal: RecoveryJournal,
    status: DrainStatus,
    expectedVersion: String
  ) async throws {
    try await UpdateLeaseCompletionBoundary.execute(
      sealed: status.sealed,
      expectedPID: status.pid,
      expectedBootID: status.bootID,
      expectedLeaseID: journal.leaseID,
      expectedVersion: expectedVersion,
      stopRenewal: { await self.stopLeaseRenewal() },
      sealLease: {
        let sealed: DrainStatus = try await self.daemonRequest(
          path: "update/seal",
          method: "POST",
          leaseID: journal.leaseID,
          decode: DrainStatus.self
        )
        return ReplacementSealStatus(
          state: sealed.state,
          pid: sealed.pid,
          bootID: sealed.bootID,
          activeTotal: sealed.activeTotal,
          leaseID: sealed.leaseID,
          sealed: sealed.sealed,
          version: sealed.version
        )
      },
      confirmBootstrap: {
        let confirmed: DrainStatus = try await self.daemonRequest(
          path: "update/confirm",
          method: "POST",
          leaseID: journal.leaseID,
          expectedBootID: status.bootID,
          decode: DrainStatus.self
        )
        return ConfirmedBootstrapStatus(
          pid: confirmed.pid,
          bootID: confirmed.bootID,
          leaseID: confirmed.leaseID,
          sealed: confirmed.sealed,
          bootstrapAuthorized: confirmed.bootstrapAuthorized,
          version: confirmed.version
        )
      },
      waitForExactHealth: {
        try await self.waitForDaemonHealth(
          journal,
          status: status,
          expectedVersion: expectedVersion
        )
      },
      cancelLease: {
        try await self.cancelUpdateLease(
          journal.leaseID,
          expectedBootID: status.bootID
        )
      }
    )
  }

  private func waitForDaemonHealth(
    _ journal: RecoveryJournal,
    status: DrainStatus,
    expectedVersion: String
  ) async throws {
    do {
      try await UpdateRecoveryDeadline.execute(
        timeoutNanoseconds: recoveryTimeout
      ) { [self] in
        try await UpdateRecoveryPolling.untilSuccess(
          delayNanoseconds: stopPollDelay
        ) {
          try await verifyReleasedDaemonHealth(
            journal,
            status: status,
            expectedVersion: expectedVersion
          )
        }
      }
    } catch UpdateRecoveryDeadlineError.timedOut {
      throw CoordinatorError.daemonDidNotRecover(
        "the released daemon did not become healthy before the recovery deadline"
      )
    }
  }

  private func verifyReleasedDaemonHealth(
    _ journal: RecoveryJournal,
    status: DrainStatus,
    expectedVersion: String
  ) async throws {
    let metadata = try stoppedMetadata(journal)
    if metadata.wasLoaded {
      guard let launchAgent = try await inspectLaunchAgent() else {
        throw CoordinatorError.launchAgentRestore("the released job is not loaded")
      }
      try verifyLaunchAgent(launchAgent, daemonPID: status.pid)
    } else {
      guard try await inspectLaunchAgent() == nil else {
        throw CoordinatorError.daemonDidNotRecover(
          "a LaunchAgent was loaded for a previously detached daemon"
        )
      }
      try verifyProcessExecutable(status.pid)
    }

    let health: HealthStatus = try await daemonRequest(
      path: "health",
      method: "GET",
      leaseID: nil,
      decode: HealthStatus.self
    )
    guard health.version == expectedVersion else {
      throw CoordinatorError.daemonDidNotRecover(
        "released daemon version \(health.version ?? "missing") does not match \(expectedVersion)"
      )
    }
  }

  private func requestFlutterDaemonRestart() async throws {
    guard let channel else {
      throw CoordinatorError.daemonDidNotRecover("the Flutter restart channel is unavailable")
    }
    try await UpdateCallbackDeadline.execute(
      timeoutNanoseconds: UInt64(commandTimeout * 1_000_000_000)
    ) { completion in
      channel.invokeMethod("restartDaemonAfterUpdateAbort", arguments: nil) { response in
        if let error = response as? FlutterError {
          completion(
            .failure(
              CoordinatorError.daemonDidNotRecover(
              error.message ?? "Flutter rejected the daemon restart"
            )
            )
          )
        } else {
          completion(.success(()))
        }
      }
    }
  }

  // MARK: - Durable recovery journal

  private var journalURL: URL {
    get throws {
      guard let dataDirectory else {
        throw CoordinatorError.invalidConfiguration("data directory is not configured")
      }
      return dataDirectory.appendingPathComponent(journalFilename, isDirectory: false)
    }
  }

  private var journalStore: UpdateRecoveryJournalStore {
    get throws { UpdateRecoveryJournalStore(url: try journalURL) }
  }

  private func readJournal() throws -> RecoveryJournal? {
    do {
      return try journalStore.read()
    } catch {
      throw CoordinatorError.journal(error.localizedDescription)
    }
  }

  private func writeJournal(_ journal: RecoveryJournal) throws {
    do {
      try journalStore.write(journal)
    } catch {
      throw CoordinatorError.journal(error.localizedDescription)
    }
  }

  private func clearJournal() throws {
    do {
      try journalStore.clear()
    } catch {
      throw CoordinatorError.journal(error.localizedDescription)
    }
  }

  private func currentBundleVersion() throws -> String {
    guard
      let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString")
        as? String,
      !version.isEmpty
    else {
      throw CoordinatorError.invalidConfiguration("the app bundle has no version")
    }
    return version
  }

  // MARK: - Daemon drain and stop

  private func drainDaemonWork(leaseID: String) async throws -> DrainStatus {
    do {
      return try await UpdateDrainDeadline.execute(
        timeoutNanoseconds: drainTimeout
      ) { [weak self] in
        guard let self else { throw CancellationError() }
        var originalPID: Int32?
        while true {
          try Task.checkCancellation()
          let status = try await self.prepareDaemon(leaseID: leaseID)
          guard status.pid > 0 else {
            throw CoordinatorError.daemonRequest("the daemon returned an invalid PID")
          }
          if let originalPID, originalPID != status.pid {
            throw CoordinatorError.daemonIdentityChanged(originalPID, status.pid)
          }
          originalPID = status.pid
          if status.activeTotal == 0 && status.state == "ready" {
            return status
          }
          try await Task.sleep(nanoseconds: self.drainRenewalDelay)
        }
      }
    } catch UpdateDrainDeadlineError.timedOut {
      throw CoordinatorError.daemonRequest(
        "active daemon work did not drain within 10 minutes; the update was cancelled"
      )
    }
  }

  private func prepareDaemon(leaseID: String) async throws -> DrainStatus {
    let status: DrainStatus = try await daemonRequest(
      path: "update/prepare",
      method: "POST",
      leaseID: leaseID,
      decode: DrainStatus.self
    )
    guard status.leaseID == leaseID else {
      throw CoordinatorError.leaseIdentityChanged(leaseID, status.leaseID)
    }
    guard !status.bootID.isEmpty else {
      throw CoordinatorError.daemonRequest("the daemon returned an empty boot identity")
    }
    return status
  }

  private func startLeaseRenewal(
    leaseID: String,
    daemonPID: Int32,
    daemonBootID: String
  ) {
    renewalTask?.cancel()
    renewalFailure = nil
    renewalTask = Task { @MainActor [weak self] in
      guard let self else { return }
      while !Task.isCancelled {
        do {
          try await Task.sleep(nanoseconds: drainRenewalDelay)
          let status = try await prepareDaemon(leaseID: leaseID)
          guard status.pid == daemonPID else {
            throw CoordinatorError.daemonIdentityChanged(daemonPID, status.pid)
          }
          guard status.bootID == daemonBootID else {
            throw CoordinatorError.daemonRequest(
              "daemon boot identity changed while the update lease was held"
            )
          }
          guard status.state == "ready", status.activeTotal == 0 else {
            throw CoordinatorError.daemonRequest(
              "new work appeared while the update lease was held"
            )
          }
        } catch is CancellationError {
          return
        } catch {
          if daemonStopVerified
            || (!Self.processIsAlive(daemonPID) && (try? daemonLockIsAvailable()) == true)
          {
            return
          }
          renewalFailure = error
          return
        }
      }
    }
  }

  private func stopLeaseRenewal() async {
    let task = renewalTask
    renewalTask = nil
    task?.cancel()
    _ = await task?.value
  }

  private func persistSealAndStopDaemon(
    journal: RecoveryJournal,
    status: DrainStatus,
    launchAgent: LaunchAgentSnapshot?,
    expectedRunningVersion: String,
    shouldStopDaemon: Bool = true
  ) async throws {
    try await UpdateReplacementBoundary.execute(
      expectedPID: status.pid,
      expectedBootID: status.bootID,
      expectedLeaseID: journal.leaseID,
      expectedVersion: expectedRunningVersion,
      persistJournal: { try self.writeJournal(journal) },
      stopRenewal: {
        await self.stopLeaseRenewal()
        self.renewalFailure = nil
      },
      seal: {
        let sealed: DrainStatus = try await self.daemonRequest(
          path: "update/seal",
          method: "POST",
          leaseID: journal.leaseID,
          decode: DrainStatus.self
        )
        return ReplacementSealStatus(
          state: sealed.state,
          pid: sealed.pid,
          bootID: sealed.bootID,
          activeTotal: sealed.activeTotal,
          leaseID: sealed.leaseID,
          sealed: sealed.sealed,
          version: sealed.version
        )
      },
      persistSealedJournal: {
        var sealedJournal = journal
        sealedJournal.phase = .sealed
        try self.writeJournal(sealedJournal)
      },
      stopDaemon: {
        if shouldStopDaemon {
          try await self.stopDaemon(
            status: status,
            leaseID: journal.leaseID,
            launchAgent: launchAgent
          )
        }
      }
    )
  }

  private func stopDaemon(
    status: DrainStatus,
    leaseID: String,
    launchAgent: LaunchAgentSnapshot?
  ) async throws {
    // persistSealAndStopDaemon has already fsynced the recovery journal,
    // stopped the network renewer, and authenticated a non-expiring idle seal.
    // From here onward no daemon work can be admitted until owner DELETE.
    try Task.checkCancellation()

    if launchAgent != nil {
      guard let current = try await inspectLaunchAgent() else {
        throw CoordinatorError.launchAgentInspection("job unloaded before the verified bootout")
      }
      try verifyLaunchAgent(current, daemonPID: status.pid)
      let output = try await runProcess(
        "/bin/launchctl",
        ["bootout", launchAgentTarget],
        timeout: commandTimeout
      )
      if output.status != 0, try await inspectLaunchAgent() != nil {
        throw CoordinatorError.launchAgentInspection("bootout failed: \(output.details)")
      }
      guard try await inspectLaunchAgent() == nil else {
        throw CoordinatorError.launchAgentInspection("job remained loaded after bootout")
      }
      daemonStoppedForUpdate = true
    } else {
      try verifyProcessExecutable(status.pid)
      let _: EmptyResponse = try await daemonRequest(
        path: "shutdown",
        method: "POST",
        leaseID: leaseID,
        decode: EmptyResponse.self
      )
      daemonStoppedForUpdate = true
    }

    for _ in 0..<stopPollAttempts {
      if !Self.processIsAlive(status.pid), try daemonLockIsAvailable() {
        return
      }
      try await Task.sleep(nanoseconds: stopPollDelay)
    }
    throw CoordinatorError.daemonDidNotStop(status.pid)
  }

  private func cancelUnsealedUpdateLease(_ leaseID: String) async throws {
    let current = try await prepareDaemon(leaseID: leaseID)
    guard !current.sealed else {
      throw CoordinatorError.daemonRequest(
        "a sealed update lease requires authenticated bootstrap recovery before cancellation"
      )
    }
    try await cancelUpdateLease(leaseID, expectedBootID: current.bootID)
  }

  private func cancelUpdateLease(
    _ leaseID: String,
    expectedBootID: String
  ) async throws {
    let status: DrainStatus = try await daemonRequest(
      path: "update/prepare",
      method: "DELETE",
      leaseID: leaseID,
      expectedBootID: expectedBootID,
      decode: DrainStatus.self
    )
    guard status.leaseID == leaseID else {
      throw CoordinatorError.leaseIdentityChanged(leaseID, status.leaseID)
    }
    guard status.bootID == expectedBootID else {
      throw CoordinatorError.daemonRequest(
        "daemon boot identity changed while cancelling the update lease"
      )
    }
  }

  private func daemonRequest<T: Decodable>(
    path: String,
    method: String,
    leaseID: String?,
    expectedBootID: String? = nil,
    decode: T.Type
  ) async throws -> T {
    guard let baseURL = apiBaseURL else {
      throw CoordinatorError.invalidConfiguration("daemon endpoint is not configured")
    }
    guard let token = currentAPIToken(), !token.isEmpty else {
      throw CoordinatorError.invalidConfiguration("daemon API token is unavailable")
    }

    let url = path.split(separator: "/").reduce(baseURL) { partial, component in
      partial.appendingPathComponent(String(component))
    }
    var request = URLRequest(url: url)
    request.httpMethod = method
    request.timeoutInterval = commandTimeout
    request.setValue(token, forHTTPHeaderField: "X-Heimdallm-Token")
    request.setValue("application/json", forHTTPHeaderField: "Accept")
    if let leaseID {
      request.setValue(leaseID, forHTTPHeaderField: updateLeaseHeader)
    }
    if let expectedBootID {
      request.setValue(expectedBootID, forHTTPHeaderField: expectedBootIDHeader)
    }

    let (data, response) = try await perform(request)
    guard (200..<300).contains(response.statusCode) else {
      let body = String(data: data, encoding: .utf8)?.trimmingCharacters(
        in: .whitespacesAndNewlines
      )
      throw CoordinatorError.daemonRequest(
        body?.isEmpty == false ? "HTTP \(response.statusCode): \(body!)" : "HTTP \(response.statusCode)"
      )
    }
    do {
      return try JSONDecoder().decode(T.self, from: data)
    } catch {
      throw CoordinatorError.daemonRequest("invalid response: \(error.localizedDescription)")
    }
  }

  private func perform(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
    let cancellable = CancellableDataTask()
    return try await withTaskCancellationHandler {
      try Task.checkCancellation()
      return try await withCheckedThrowingContinuation { continuation in
        let task = URLSession.shared.dataTask(with: request) { data, response, error in
          if let error {
            continuation.resume(throwing: error)
            return
          }
          guard let response = response as? HTTPURLResponse else {
            continuation.resume(
              throwing: CoordinatorError.daemonRequest("the daemon returned no HTTP response")
            )
            return
          }
          continuation.resume(returning: (data ?? Data(), response))
        }
        cancellable.resume(task)
      }
    } onCancel: {
      cancellable.cancel()
    }
  }

  private func currentAPIToken() -> String? {
    UpdateAPITokenProvider(path: apiTokenPath, fallback: fallbackAPIToken).current()
  }

  private func daemonLockIsAvailable() throws -> Bool {
    guard let dataDirectory else {
      throw CoordinatorError.invalidConfiguration("data directory is not configured")
    }
    let path = dataDirectory.appendingPathComponent("daemon.lock").path
    if !FileManager.default.fileExists(atPath: path) { return true }
    let descriptor = Darwin.open(path, O_RDWR | O_NOFOLLOW)
    guard descriptor >= 0 else {
      throw CoordinatorError.daemonRequest("could not open daemon lifecycle lock")
    }
    defer { _ = Darwin.close(descriptor) }
    if flock(descriptor, LOCK_EX | LOCK_NB) != 0 { return false }
    _ = flock(descriptor, LOCK_UN)
    return true
  }

  private func sealUpdateBarrierWhileDaemonAbsent(leaseID: String) throws {
    guard let dataDirectory else {
      throw CoordinatorError.invalidConfiguration("data directory is not configured")
    }
    try UpdateAbsentDaemonSealBoundary(
      lockURL: dataDirectory.appendingPathComponent("daemon.lock", isDirectory: false),
      markerURL: dataDirectory.appendingPathComponent("update-drain.json", isDirectory: false)
    ).seal(leaseID: leaseID)
  }

  private nonisolated static func processIsAlive(_ pid: Int32) -> Bool {
    if Darwin.kill(pid_t(pid), 0) == 0 { return true }
    return errno == EPERM
  }

  private func verifyProcessExecutable(_ pid: Int32) throws {
    var buffer = [UInt8](repeating: 0, count: 4 * Int(MAXPATHLEN))
    let length = buffer.withUnsafeMutableBytes { bytes in
      Darwin.proc_pidpath(pid, bytes.baseAddress, UInt32(bytes.count))
    }
    guard length > 0 else {
      throw CoordinatorError.daemonIdentityChanged(pid, 0)
    }
    let actual = buffer.withUnsafeBufferPointer { pointer in
      String(cString: UnsafePointer<CChar>(OpaquePointer(pointer.baseAddress!)))
    }
    guard normalizedPath(actual) == normalizedPath(expectedDaemonPath) else {
      throw CoordinatorError.daemonRequest(
        "PID \(pid) runs \(actual), not the bundled daemon \(expectedDaemonPath)"
      )
    }
  }

  // MARK: - LaunchAgent state

  private var launchDomain: String { "gui/\(getuid())" }
  private var launchAgentTarget: String { "\(launchDomain)/\(launchAgentLabel)" }
  private var launchAgentPlist: String {
    FileManager.default.homeDirectoryForCurrentUser
      .appendingPathComponent("Library/LaunchAgents/\(launchAgentLabel).plist").path
  }
  private var expectedDaemonPath: String {
    Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/heimdalld").path
  }

  private func inspectLaunchAgent() async throws -> LaunchAgentSnapshot? {
    let output = try await runProcess(
      "/bin/launchctl",
      ["print", launchAgentTarget],
      timeout: commandTimeout
    )
    if output.status != 0 {
      let details = "\(output.stdout)\n\(output.stderr)".lowercased()
      if output.status == 113 || details.contains("could not find service") {
        return nil
      }
      throw CoordinatorError.launchAgentInspection(output.details)
    }

    do {
      return try LaunchAgentSnapshot.parse(launchctlPrint: output.stdout)
    } catch {
      throw CoordinatorError.launchAgentInspection(
        error.localizedDescription
      )
    }
  }

  private func verifyLaunchAgent(_ snapshot: LaunchAgentSnapshot, daemonPID: Int32) throws {
    guard normalizedPath(snapshot.plistPath) == normalizedPath(launchAgentPlist) else {
      throw CoordinatorError.launchAgentInspection(
        "loaded job came from \(snapshot.plistPath), not \(launchAgentPlist)"
      )
    }
    guard normalizedPath(snapshot.programPath) == normalizedPath(expectedDaemonPath) else {
      throw CoordinatorError.launchAgentInspection(
        "loaded job runs \(snapshot.programPath), not \(expectedDaemonPath)"
      )
    }
    guard snapshot.pid == daemonPID else {
      throw CoordinatorError.daemonIdentityChanged(daemonPID, snapshot.pid)
    }
    try verifyProcessExecutable(snapshot.pid)
  }

  private func validateCanonicalPlist() throws {
    var fileInfo = stat()
    guard Darwin.lstat(launchAgentPlist, &fileInfo) == 0 else {
      throw CoordinatorError.launchAgentRestore("plist is missing at \(launchAgentPlist)")
    }
    guard (fileInfo.st_mode & S_IFMT) == S_IFREG, fileInfo.st_uid == getuid() else {
      throw CoordinatorError.launchAgentRestore(
        "plist must be a regular file owned by the current user"
      )
    }
    do {
      let data = try Data(contentsOf: URL(fileURLWithPath: launchAgentPlist))
      guard
        let plist = try PropertyListSerialization.propertyList(from: data, format: nil)
          as? [String: Any],
        plist["Label"] as? String == launchAgentLabel,
        let arguments = plist["ProgramArguments"] as? [String],
        let program = arguments.first,
        normalizedPath(program) == normalizedPath(expectedDaemonPath)
      else {
        throw CoordinatorError.launchAgentRestore(
          "plist label or ProgramArguments[0] does not match the installed daemon"
        )
      }
    } catch let error as CoordinatorError {
      throw error
    } catch {
      throw CoordinatorError.launchAgentRestore(error.localizedDescription)
    }
  }

  private func launchAgentIsDisabled() async throws -> Bool {
    let output = try await runProcess(
      "/bin/launchctl",
      ["print-disabled", launchDomain],
      timeout: commandTimeout
    )
    guard output.status == 0 else {
      throw CoordinatorError.launchAgentInspection(output.details)
    }
    let escapedLabel = NSRegularExpression.escapedPattern(for: launchAgentLabel)
    let pattern = "[\\\"']?\(escapedLabel)[\\\"']?\\s*=>\\s*(true|disabled)"
    return output.stdout.range(of: pattern, options: [.regularExpression, .caseInsensitive]) != nil
  }

  private func restoreLaunchAgent(disabled: Bool) async throws {
    try validateCanonicalPlist()
    if try await inspectLaunchAgent() == nil {
      if disabled {
        let enable = try await runProcess(
          "/bin/launchctl",
          ["enable", launchAgentTarget],
          timeout: commandTimeout
        )
        guard enable.status == 0 else {
          throw CoordinatorError.launchAgentRestore(enable.details)
        }
      }
      let bootstrap = try await runProcess(
        "/bin/launchctl",
        ["bootstrap", launchDomain, launchAgentPlist],
        timeout: commandTimeout
      )
      if bootstrap.status != 0, try await inspectLaunchAgent() == nil {
        throw CoordinatorError.launchAgentRestore(bootstrap.details)
      }
    }

    let stateCommand = disabled ? "disable" : "enable"
    let state = try await runProcess(
      "/bin/launchctl",
      [stateCommand, launchAgentTarget],
      timeout: commandTimeout
    )
    guard state.status == 0 else {
      throw CoordinatorError.launchAgentRestore(state.details)
    }
    guard let restored = try await inspectLaunchAgent() else {
      throw CoordinatorError.launchAgentRestore("job is not loaded after bootstrap")
    }
    guard normalizedPath(restored.plistPath) == normalizedPath(launchAgentPlist),
      normalizedPath(restored.programPath) == normalizedPath(expectedDaemonPath)
    else {
      throw CoordinatorError.launchAgentRestore("restored job identity does not match the bundle")
    }
  }

  private func normalizedPath(_ path: String) -> String {
    URL(fileURLWithPath: path).standardizedFileURL.resolvingSymlinksInPath().path
  }

  // MARK: - Process execution

  private func runProcess(
    _ executable: String,
    _ arguments: [String],
    timeout: TimeInterval
  ) async throws -> CommandOutput {
    let process = Process()
    let stdout = Pipe()
    let stderr = Pipe()
    process.executableURL = URL(fileURLWithPath: executable)
    process.arguments = arguments
    process.standardOutput = stdout
    process.standardError = stderr
    let running = RunningProcess(process)

    return try await withTaskCancellationHandler {
      try Task.checkCancellation()
      try process.run()

      let stdoutTask = Task.detached(priority: .utility) {
        stdout.fileHandleForReading.readDataToEndOfFile()
      }
      let stderrTask = Task.detached(priority: .utility) {
        stderr.fileHandleForReading.readDataToEndOfFile()
      }
      let exitTask = Task.detached(priority: .utility) {
        process.waitUntilExit()
        return process.terminationStatus
      }

      do {
        let status = try await withThrowingTaskGroup(of: Int32.self) { group in
          group.addTask { await exitTask.value }
          group.addTask {
            try await Task.sleep(nanoseconds: UInt64(timeout * 1_000_000_000))
            running.stopAndEscalate()
            throw CoordinatorError.processTimedOut(executable)
          }
          guard let first = try await group.next() else {
            throw CoordinatorError.processTimedOut(executable)
          }
          group.cancelAll()
          return first
        }
        let outData = await stdoutTask.value
        let errorData = await stderrTask.value
        return CommandOutput(
          status: status,
          stdout: String(data: outData, encoding: .utf8) ?? "",
          stderr: String(data: errorData, encoding: .utf8) ?? ""
        )
      } catch {
        running.stopAndEscalate()
        _ = await exitTask.value
        _ = await stdoutTask.value
        _ = await stderrTask.value
        throw error
      }
    } onCancel: {
      running.stopAndEscalate()
    }
  }

  private func flutterError(_ error: Error) -> FlutterError {
    FlutterError(
      code: "app_update_failed",
      message: error.localizedDescription,
      details: nil
    )
  }
}
