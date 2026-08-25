import Foundation
import Testing
@testable import PunchcardBar

// The loopback listener speaks the smallest possible HTTP. Parsing the request
// line is the one piece of that worth pinning down.
@Test func parsesTheCodeFromTheCallbackRequest() {
    let request = "GET /callback?code=abc123 HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"
    #expect(Login.parseCode(fromRequestLine: request) == "abc123")
}

@Test func rejectsACallbackWithoutACode() {
    #expect(Login.parseCode(fromRequestLine: "GET /callback?error=denied HTTP/1.1\r\n\r\n") == nil)
    #expect(Login.parseCode(fromRequestLine: "GET /callback HTTP/1.1\r\n\r\n") == nil)
    #expect(Login.parseCode(fromRequestLine: "") == nil)
}

@Test func rejectsAnEmptyCode() {
    #expect(Login.parseCode(fromRequestLine: "GET /callback?code= HTTP/1.1\r\n\r\n") == nil)
}

// The listener has to report the port the OS actually gave it.
//
// NWListener created with `.any` answers `port` with 0 — not nil — until it
// reaches .ready. A nil check therefore passes immediately and the callback URL
// comes out as 127.0.0.1:0, which every browser refuses: ERR_UNSAFE_PORT. That
// is how this shipped, and it is why readiness is awaited rather than polled.
@Test func listenerReportsARealPort() async throws {
    let (port, _) = try await Login.startListener()
    defer { Login.stopListener() }
    #expect(port != 0)
    #expect(port > 1024)
}
