import Foundation
import Testing
@testable import PunchcardBar

// The queue file is a contract with the hook and with the Go CLI, both of which
// can be writing while this app reads. These tests are about that protocol, not
// about the network: everything below stops at the point a request would go out.

private func makeDir() throws -> URL {
    let dir = FileManager.default.temporaryDirectory
        .appendingPathComponent("punchcard-queue-\(UUID().uuidString)")
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    return dir
}

private func write(_ lines: [String], to dir: URL) throws {
    try lines.joined(separator: "\n").appending("\n")
        .write(to: Queue.queueFile(in: dir), atomically: true, encoding: .utf8)
}

private func line(_ id: String) -> String {
    """
    {"tool":"claude-code","external_id":"\(id)","started_at":"2026-08-27T10:00:00Z","ended_at":"2026-08-27T10:05:00Z"}
    """
}

@Test func pendingCountsWhatIsWaiting() throws {
    let dir = try makeDir()
    #expect(Queue.pendingCount(in: dir) == 0)
    try write([line("a"), line("b"), line("c")], to: dir)
    #expect(Queue.pendingCount(in: dir) == 3)
}

// A line the server could never accept would otherwise block the queue forever,
// so it is counted out rather than retried. `tool` and `external_id` are the two
// the server cannot do without — the same check the CLI makes.
@Test func unusableLinesAreDroppedRatherThanBlocking() throws {
    let dir = try makeDir()
    try write([
        line("good"),
        "{not json at all",
        #"{"tool":"claude-code","started_at":"2026-08-27T10:00:00Z"}"#,  // no external_id
        #"{"tool":"","external_id":"x"}"#,                               // no tool
        "",
        line("also-good"),
    ], to: dir)
    #expect(Queue.pendingCount(in: dir) == 2)
}

// Fields this app has never heard of belong to whoever wrote the line. Sending
// is forwarding, not re-authoring, so a newer hook's extra field survives.
@Test func unknownFieldsSurviveARoundTrip() throws {
    let dir = try makeDir()
    let rich = #"{"tool":"codex","external_id":"z","started_at":"2026-08-27T10:00:00Z","ended_at":"2026-08-27T10:05:00Z","something_new":42}"#
    try write([rich], to: dir)

    guard let claim = Queue.claim(in: dir) else {
        Issue.record("nothing claimed")
        return
    }
    Queue.putBack(claim.runs, in: dir)
    let back = Queue.readRuns(at: Queue.queueFile(in: dir))
    #expect(back.count == 1)
    #expect(back[0]["something_new"] as? Int == 42)
}

// With one shared ".sending" name, a second claim arriving after a hook had
// recreated the queue renamed the new file over the first claim's in-flight
// batch and destroyed it. Unique names make an overlap harmless.
@Test func twoClaimsCarryDisjointHalves() throws {
    let dir = try makeDir()
    try write([line("first")], to: dir)

    guard let first = Queue.claim(in: dir) else {
        Issue.record("first claim took nothing")
        return
    }
    #expect(first.runs.count == 1)

    // A hook appends while the first flush is still sending.
    try write([line("second")], to: dir)
    guard let second = Queue.claim(in: dir) else {
        Issue.record("second claim took nothing")
        return
    }
    #expect(second.runs.count == 1)
    #expect(second.runs[0]["external_id"] as? String == "second")
    #expect(first.file != second.file)
    #expect(FileManager.default.fileExists(atPath: first.file.path))
}

@Test func claimingAnEmptyQueueTakesNothing() throws {
    let dir = try makeDir()
    #expect(Queue.claim(in: dir) == nil)
}

// A flush killed between claiming and sending used to take its batch with it
// silently. Old batches come back; one that may still be in flight does not.
@Test func abandonedBatchesComeBackOnceTheyAreOldEnough() throws {
    let dir = try makeDir()
    try write([line("abandoned")], to: dir)
    guard let claim = Queue.claim(in: dir) else {
        Issue.record("nothing claimed")
        return
    }

    Queue.recoverOrphans(in: dir)
    #expect(Queue.pendingCount(in: dir) == 0)  // still fresh; someone may be sending it

    let old = Date().addingTimeInterval(-2 * Queue.orphanAfter)
    try FileManager.default.setAttributes([.modificationDate: old], ofItemAtPath: claim.file.path)
    Queue.recoverOrphans(in: dir)
    #expect(Queue.pendingCount(in: dir) == 1)
    #expect(!FileManager.default.fileExists(atPath: claim.file.path))
}

// Order is the point of putting things back at the front: a run recorded before
// the flush began must not end up behind one recorded during it.
@Test func returnedRunsGoAheadOfWhatArrivedMeanwhile() throws {
    let dir = try makeDir()
    try write([line("during-the-flush")], to: dir)

    let returned = Queue.readRuns(at: Queue.queueFile(in: dir))
    #expect(returned.count == 1)

    Queue.putBack([["tool": "claude-code", "external_id": "before-the-flush"]], in: dir)
    let all = Queue.readRuns(at: Queue.queueFile(in: dir))
    #expect(all.count == 2)
    #expect(all[0]["external_id"] as? String == "before-the-flush")
    #expect(all[1]["external_id"] as? String == "during-the-flush")
}
