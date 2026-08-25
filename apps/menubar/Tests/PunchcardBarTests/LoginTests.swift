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
