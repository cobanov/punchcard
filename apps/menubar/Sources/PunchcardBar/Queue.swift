import Foundation

/// The local queue of agent turns, read from the same file the CLI writes.
///
/// The hook appends one JSON object per finished turn and returns; something
/// else has to send them. For a while that something was a human remembering to
/// type `punchcard sync`, and the result was seventy-one turns sitting unsent
/// for two days with nothing anywhere saying so. The CLI now drains the queue on
/// its own, but a Mac where the menu bar app is the only punchcard the user ever
/// touches would still go quiet — so this app drains it too, and, unlike the
/// CLI, can show the backlog before anyone goes looking for it.
///
/// The on-disk protocol is shared with the Go implementation and must stay
/// identical, because both can be running: claim by renaming the queue aside
/// under a name unique to this process, send, then delete. A hook appending
/// during a flush writes to a fresh file this flush never had.
enum Queue {

    /// Batches are sent in the same chunk size the CLI uses — the server's cap.
    private static let batchSize = 200

    /// How long a claimed batch may sit before it is presumed abandoned.
    ///
    /// Matches the CLI. Long enough that a flush in progress is never stolen,
    /// and re-sending is free anyway: `external_id` is the idempotency key.
    static let orphanAfter: TimeInterval = 10 * 60

    /// Where the queue lives — `$XDG_STATE_HOME/punchcard`, else
    /// `~/.local/state/punchcard`. Not the app's container: this is a contract
    /// with the hook, and the hook is not a Mac app.
    static var directory: URL {
        let base = ProcessInfo.processInfo.environment["XDG_STATE_HOME"].flatMap {
            $0.isEmpty ? nil : URL(fileURLWithPath: $0)
        } ?? FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".local/state")
        return base.appendingPathComponent("punchcard")
    }

    static func queueFile(in dir: URL) -> URL { dir.appendingPathComponent("queue.jsonl") }

    /// How many turns are waiting. Counted, not estimated — the number is shown
    /// to someone deciding whether something is broken.
    static func pendingCount(in dir: URL = directory) -> Int {
        readRuns(at: queueFile(in: dir)).count
    }

    /// Sends everything waiting, and reports what went.
    ///
    /// Returns nil when there was nothing to do, so a caller can tell "up to
    /// date" from "sent nothing because the server refused".
    @MainActor
    static func flush(using api: API, in dir: URL = directory) async throws -> Int? {
        recoverOrphans(in: dir)

        guard let claim = claim(in: dir) else { return nil }
        var sent = 0
        var index = 0
        while index < claim.runs.count {
            let end = min(index + batchSize, claim.runs.count)
            let slice = Array(claim.runs[index..<end])
            do {
                let body = try JSONSerialization.data(withJSONObject: ["runs": slice])
                sent += try await api.recordAgentRuns(body: body)
            } catch {
                // Everything from this batch on goes back to the front, so a
                // server that is down or a token that expired costs a retry and
                // nothing more.
                putBack(Array(claim.runs[index...]), in: dir)
                try? FileManager.default.removeItem(at: claim.file)
                throw error
            }
            index = end
        }
        try? FileManager.default.removeItem(at: claim.file)
        return sent
    }

    // MARK: - The file protocol

    // The three steps below are the protocol itself, shared with the Go
    // implementation byte for byte, so they are reachable from tests rather than
    // private: a divergence here corrupts a file both clients write.

    struct Claim {
        let runs: [[String: Any]]
        let file: URL
    }

    /// Moves the queue aside under a name nothing else can be holding.
    ///
    /// The name carries this process's pid and the moment it claimed. With one
    /// shared ".sending" name, a second syncer arriving after a hook had
    /// recreated the queue would rename the new file over the first syncer's
    /// in-flight batch and destroy it. Unique names make an overlap harmless:
    /// the two syncers carry disjoint halves.
    static func claim(in dir: URL) -> Claim? {
        let pid = ProcessInfo.processInfo.processIdentifier
        let stamp = Int(Date().timeIntervalSince1970 * 1_000_000_000)
        let batch = dir.appendingPathComponent("queue.jsonl.sending.\(pid)-\(stamp)")
        do {
            try FileManager.default.moveItem(at: queueFile(in: dir), to: batch)
        } catch {
            return nil  // nothing queued, or another syncer got there first
        }
        let runs = readRuns(at: batch)
        guard !runs.isEmpty else {
            try? FileManager.default.removeItem(at: batch)
            return nil
        }
        return Claim(runs: runs, file: batch)
    }

    /// Reads a queue file, dropping lines that could never be sent.
    ///
    /// A corrupt line would otherwise block the queue forever, so it is counted
    /// out rather than retried. `tool` and `external_id` are the two fields the
    /// server cannot do without — the same check the CLI makes.
    static func readRuns(at url: URL) -> [[String: Any]] {
        guard let text = try? String(contentsOf: url, encoding: .utf8) else { return [] }
        var runs: [[String: Any]] = []
        for line in text.split(separator: "\n") {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard !trimmed.isEmpty, let data = trimmed.data(using: .utf8),
                  let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
                  let tool = object["tool"] as? String, !tool.isEmpty,
                  let id = object["external_id"] as? String, !id.isEmpty
            else { continue }
            runs.append(object)
        }
        return runs
    }

    /// Puts unsent runs back at the FRONT, ahead of anything a hook appended
    /// while this flush was away, so the queue stays in the order it happened.
    static func putBack(_ runs: [[String: Any]], in dir: URL) {
        let all = runs + readRuns(at: queueFile(in: dir))
        var text = ""
        for run in all {
            guard let data = try? JSONSerialization.data(withJSONObject: run),
                  let line = String(data: data, encoding: .utf8) else { continue }
            text += line + "\n"
        }
        guard !text.isEmpty else { return }
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        try? text.write(to: queueFile(in: dir), atomically: true, encoding: .utf8)
    }

    /// Puts back batches whose syncer died before sending them.
    ///
    /// Claiming and sending are two steps, and a process killed between them
    /// used to take its batch with it silently. Only batches past `orphanAfter`
    /// are touched, so a flush in progress is never stolen.
    static func recoverOrphans(in dir: URL) {
        let fm = FileManager.default
        guard let entries = try? fm.contentsOfDirectory(
            at: dir, includingPropertiesForKeys: [.contentModificationDateKey]) else { return }
        for entry in entries where entry.lastPathComponent.hasPrefix("queue.jsonl.sending.") {
            guard let modified = try? entry.resourceValues(forKeys: [.contentModificationDateKey])
                .contentModificationDate,
                Date().timeIntervalSince(modified) > orphanAfter else { continue }
            let runs = readRuns(at: entry)
            try? fm.removeItem(at: entry)
            if !runs.isEmpty { putBack(runs, in: dir) }
        }
    }
}
