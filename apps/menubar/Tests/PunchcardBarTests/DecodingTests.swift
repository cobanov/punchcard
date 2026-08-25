import Foundation
import Testing
@testable import PunchcardBar

// Postgres emits fractional seconds. ISO8601DateFormatter's default options
// reject them, which would make every session fail to decode and show the day
// as empty — a silent, total failure that looks like "no work recorded".
@Test func sessionDecodesFractionalSecondTimestamps() throws {
    let json = """
    {"id":"s1","project_id":"p1","note":"refactor",
     "started_at":"2026-08-25T09:48:30.123456Z","ended_at":null,
     "seconds":0,"running":true,"commit_sync_state":"pending"}
    """
    let session = try API.decoder.decode(Session.self, from: Data(json.utf8))
    #expect(session.id == "s1")
    #expect(session.running)
}

@Test func sessionDecodesWholeSecondTimestamps() throws {
    let json = """
    {"id":"s1","project_id":"p1","note":"","started_at":"2026-08-25T09:48:30Z",
     "ended_at":"2026-08-25T10:48:30Z","seconds":3600,"running":false,
     "commit_sync_state":"ok"}
    """
    let session = try API.decoder.decode(Session.self, from: Data(json.utf8))
    #expect(session.seconds == 3600)
    #expect(session.endedAt != nil)
}

// A running session's clock must come from started_at, not from the `seconds`
// the server happened to report — otherwise the display freezes between polls.
@Test func elapsedIsDerivedFromStartedAtWhileRunning() {
    let session = Session(id: "s", projectID: "p", note: "",
                          startedAt: Date().addingTimeInterval(-3600),
                          endedAt: nil, seconds: 0, running: true, syncState: nil)
    #expect(session.elapsed >= 3599)
}

@Test func elapsedUsesTheServersNumberOnceStopped() {
    let session = Session(id: "s", projectID: "p", note: "",
                          startedAt: Date().addingTimeInterval(-7200),
                          endedAt: Date(), seconds: 3600, running: false, syncState: nil)
    #expect(session.elapsed == 3600)
}

@Test func todayTotalIgnoresTheRunningSession() {
    var state = BarState()
    state.today = [
        Session(id: "a", projectID: "p", note: "", startedAt: Date(), endedAt: Date(),
                seconds: 3600, running: false, syncState: nil),
        Session(id: "b", projectID: "p", note: "", startedAt: Date(), endedAt: nil,
                seconds: 999, running: true, syncState: nil),
    ]
    #expect(state.todaySeconds == 3600)
}
