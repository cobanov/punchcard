import AppKit
import Foundation
import Network

/// Browser sign-in, the same flow the CLI uses.
///
/// Bind a loopback listener, open the browser at punchcard's GitHub sign-in with
/// `redirect_to` pointing back at it, wait for the one-time code, trade the code
/// for a device token. The token never travels through the browser, so it never
/// lands in browser history or the OS log.
///
/// This is deliberately a raw NWListener speaking the smallest possible HTTP
/// rather than a real server: it serves exactly one request, on loopback, for at
/// most a couple of minutes. Pulling in a web framework — or hand-rolling a
/// general one — to answer a single GET would be more code and more surface for
/// no benefit.
enum Login {
    /// How long to wait for the browser round trip before giving up.
    static let timeout: TimeInterval = 180

    enum LoginError: LocalizedError {
        case cancelled
        case timedOut
        case noToken
        case listener(String)

        var errorDescription: String? {
            switch self {
            case .cancelled: return "Sign-in was cancelled."
            case .timedOut: return "Timed out waiting for the browser."
            case .noToken: return "The server returned no token."
            case .listener(let why): return "Could not listen for the browser: \(why)"
            }
        }
    }

    /// Runs the whole flow and returns a device token.
    static func run(baseURL: URL) async throws -> String {
        let (port, codes) = try await startListener()
        defer { stopListener() }

        let redirect = "http://127.0.0.1:\(port)/callback"
        var components = URLComponents(url: baseURL.appendingPathComponent("/v1/auth/oauth/github"),
                                       resolvingAgainstBaseURL: false)!
        components.queryItems = [
            // The repo scope rides along, so one authorization both signs in and
            // connects commit matching.
            .init(name: "scope", value: "repo"),
            .init(name: "client", value: "menubar"),
            .init(name: "redirect_to", value: redirect),
        ]
        NSWorkspace.shared.open(components.url!)

        let code = try await withTimeout(seconds: timeout) {
            try await codes.first ?? { throw LoginError.cancelled }()
        }
        return try await exchange(baseURL: baseURL, code: code)
    }

    // MARK: - Loopback listener

    private nonisolated(unsafe) static var listener: NWListener?

    /// The listener runs on its own queue, never the main one.
    ///
    /// NWListener reports readiness and accepts connections on the queue it was
    /// started with. Using `.main` and then waiting for readiness from the main
    /// thread is a deadlock: the callback that would end the wait cannot run
    /// until the wait ends.
    private static let queue = DispatchQueue(label: "run.cobanov.punchcard.bar.login")

    static func startListener() async throws -> (UInt16, AsyncStream<String>) {
        let params = NWParameters.tcp
        params.requiredInterfaceType = .loopback
        let l: NWListener
        do {
            l = try NWListener(using: params, on: .any)
        } catch {
            throw LoginError.listener(error.localizedDescription)
        }
        listener = l

        var continuation: AsyncStream<String>.Continuation!
        let stream = AsyncStream<String> { continuation = $0 }
        let sink = continuation!

        l.newConnectionHandler = { connection in
            connection.start(queue: queue)
            connection.receive(minimumIncompleteLength: 1, maximumLength: 8192) { data, _, _, _ in
                let request = String(decoding: data ?? Data(), as: UTF8.self)
                let code = parseCode(fromRequestLine: request)
                let page = code == nil ? failedPage : donePage
                let response = """
                HTTP/1.1 200 OK\r
                Content-Type: text/html; charset=utf-8\r
                Content-Length: \(page.utf8.count)\r
                Connection: close\r
                \r
                \(page)
                """
                connection.send(content: Data(response.utf8), completion: .contentProcessed { _ in
                    connection.cancel()
                })
                if let code { sink.yield(code) }
                sink.finish()
            }
        }
        // Wait for .ready, then read the port. Before .ready the listener still
        // answers `port` — with 0, the placeholder for "any" — so a nil check
        // passes and the callback URL comes out as 127.0.0.1:0, which browsers
        // refuse outright (ERR_UNSAFE_PORT).
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
            let resumed = Resumed()
            l.stateUpdateHandler = { state in
                switch state {
                case .ready:
                    if resumed.claim() { continuation.resume() }
                case .failed(let error), .waiting(let error):
                    if resumed.claim() {
                        continuation.resume(throwing: LoginError.listener(error.localizedDescription))
                    }
                default:
                    break
                }
            }
            l.start(queue: queue)
        }

        guard let port = l.port?.rawValue, port != 0 else {
            throw LoginError.listener("the listener started without a port")
        }
        return (port, stream)
    }

    static func stopListener() {
        listener?.cancel()
        listener = nil
    }

    /// A continuation may be resumed exactly once. NWListener can report
    /// .waiting and then .failed for the same problem, so the second report has
    /// to be dropped rather than crash the app.
    private final class Resumed: @unchecked Sendable {
        private let lock = NSLock()
        private var done = false
        func claim() -> Bool {
            lock.lock(); defer { lock.unlock() }
            if done { return false }
            done = true
            return true
        }
    }

    /// Pulls `code` out of the request line of a minimal HTTP request.
    static func parseCode(fromRequestLine request: String) -> String? {
        guard let line = request.split(separator: "\r\n", maxSplits: 1).first,
              let path = line.split(separator: " ").dropFirst().first,
              let components = URLComponents(string: String(path)),
              let code = components.queryItems?.first(where: { $0.name == "code" })?.value,
              !code.isEmpty
        else { return nil }
        return code
    }

    // MARK: - Exchange

    private static func exchange(baseURL: URL, code: String) async throws -> String {
        struct Response: Codable { let token: String }
        var req = URLRequest(url: baseURL.appendingPathComponent("/v1/auth/native/exchange"))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: ["code": code])

        let (data, response) = try await URLSession.shared.data(for: req)
        guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
            let problem = try? API.decoder.decode(APIProblem.self, from: data)
            throw APIProblem(status: (response as? HTTPURLResponse)?.statusCode,
                             detail: problem?.detail ?? "the exchange failed", code: problem?.code)
        }
        let token = try JSONDecoder().decode(Response.self, from: data).token
        guard !token.isEmpty else { throw LoginError.noToken }
        return token
    }

    private static func withTimeout<T: Sendable>(seconds: TimeInterval,
                                                 _ work: @escaping @Sendable () async throws -> T) async throws -> T {
        try await withThrowingTaskGroup(of: T.self) { group in
            group.addTask { try await work() }
            group.addTask {
                try await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
                throw LoginError.timedOut
            }
            let result = try await group.next()!
            group.cancelAll()
            return result
        }
    }

    private static let donePage = """
    <!doctype html><meta charset="utf-8"><title>punchcard</title>
    <style>body{font:16px/1.6 ui-serif,Georgia,serif;margin:0;display:grid;place-items:center;
    height:100vh;background:#fbfaf8;color:#1b1a17}
    @media(prefers-color-scheme:dark){body{background:#14130f;color:#eceae4}}
    p{margin:0;text-align:center}small{color:#6b675f}</style>
    <p>Signed in.<br><small>You can close this tab — punchcard is in your menu bar.</small></p>
    """

    private static let failedPage = """
    <!doctype html><meta charset="utf-8"><title>punchcard</title>
    <style>body{font:16px/1.6 ui-serif,Georgia,serif;margin:0;display:grid;place-items:center;
    height:100vh;background:#fbfaf8;color:#1b1a17}
    @media(prefers-color-scheme:dark){body{background:#14130f;color:#eceae4}}
    p{margin:0;text-align:center}small{color:#6b675f}</style>
    <p>Sign-in did not complete.<br><small>Try again from the menu bar.</small></p>
    """
}

private extension AsyncStream where Element == String {
    /// The first value, or nil if the stream finishes empty.
    var first: String? {
        get async {
            for await value in self { return value }
            return nil
        }
    }
}
