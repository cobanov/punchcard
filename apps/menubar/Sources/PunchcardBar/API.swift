import Foundation

/// The punchcard API client.
///
/// Same public API as every other client, same bearer token. There is no
/// privileged path for the Mac app, so anything awkward here is awkward for
/// everyone and belongs fixed in the API.
struct API: Sendable {
    let baseURL: URL
    let token: String

    static let defaultBaseURL = URL(string: "https://punchcard.cobanov.run")!

    /// Decoder for the API's RFC 3339 timestamps.
    ///
    /// `.iso8601` alone rejects fractional seconds, which Postgres emits — the
    /// first version of this app failed to decode every single session for that
    /// reason, silently showing an empty day.
    static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { decoder in
            let raw = try decoder.singleValueContainer().decode(String.self)
            if let date = parse(raw) { return date }
            throw DecodingError.dataCorrupted(
                .init(codingPath: decoder.codingPath, debugDescription: "unrecognised timestamp: \(raw)"))
        }
        return d
    }()

    /// ISO8601DateFormatter is not Sendable, so a shared static instance is a
    /// data race the compiler refuses. Building one per call is cheap next to
    /// the network request it accompanies, and correct without a lock.
    static func parse(_ raw: String) -> Date? {
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = fractional.date(from: raw) { return date }
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        return plain.date(from: raw)
    }

    static func stamp(_ date: Date) -> String {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f.string(from: date)
    }

    // MARK: - Reads

    func projects() async throws -> [Project] {
        struct Wrapper: Codable { let projects: [Project] }
        return try await get("/v1/projects", as: Wrapper.self).projects
    }

    /// The running session, or nil when the timer is not running.
    ///
    /// The API answers 404 for "nothing running", which is a state rather than
    /// a failure — turning it into nil here keeps that distinction out of every
    /// call site.
    func current() async throws -> Session? {
        do {
            return try await get("/v1/sessions/current", as: Session.self)
        } catch let problem as APIProblem where problem.status == 404 {
            return nil
        }
    }

    func sessions(since: Date) async throws -> [Session] {
        struct Wrapper: Codable { let sessions: [Session] }
        let from = Self.stamp(since)
        let to = Self.stamp(Date().addingTimeInterval(60))
        let path = "/v1/sessions?from=\(esc(from))&to=\(esc(to))"
        return try await get(path, as: Wrapper.self).sessions
    }

    func commits(sessionID: String) async throws -> [Commit] {
        struct Wrapper: Codable { let commits: [Commit] }
        return try await get("/v1/sessions/\(sessionID)/commits", as: Wrapper.self).commits
    }

    func github() async throws -> GitHubStatus {
        try await get("/v1/github/status", as: GitHubStatus.self)
    }

    // MARK: - Writes

    func start(projectID: String, note: String) async throws -> Session {
        try await post("/v1/sessions", body: ["project_id": projectID, "note": note], as: Session.self)
    }

    func stop(sessionID: String) async throws -> Session {
        try await post("/v1/sessions/\(sessionID)/stop", body: [:], as: Session.self)
    }

    // MARK: - Transport

    private func get<T: Decodable>(_ path: String, as type: T.Type) async throws -> T {
        try await send(request(path, method: "GET", body: nil), as: type)
    }

    private func post<T: Decodable>(_ path: String, body: [String: String], as type: T.Type) async throws -> T {
        let payload = try JSONSerialization.data(withJSONObject: body)
        return try await send(request(path, method: "POST", body: payload), as: type)
    }

    private func request(_ path: String, method: String, body: Data?) -> URLRequest {
        var req = URLRequest(url: URL(string: path, relativeTo: baseURL)!)
        req.httpMethod = method
        req.setValue("application/json", forHTTPHeaderField: "Accept")
        req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        if let body {
            req.httpBody = body
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        req.timeoutInterval = 20
        return req
    }

    private func send<T: Decodable>(_ req: URLRequest, as type: T.Type) async throws -> T {
        let (data, response) = try await URLSession.shared.data(for: req)
        guard let http = response as? HTTPURLResponse else {
            throw APIProblem(status: nil, detail: "no response", code: nil)
        }
        guard (200..<300).contains(http.statusCode) else {
            // Keep the server's own wording: "GitHub rejected the stored token;
            // reconnect GitHub" is actionable, "request failed" is not.
            if let problem = try? Self.decoder.decode(APIProblem.self, from: data) {
                throw APIProblem(status: http.statusCode, detail: problem.detail, code: problem.code)
            }
            throw APIProblem(status: http.statusCode, detail: HTTPURLResponse.localizedString(
                forStatusCode: http.statusCode), code: nil)
        }
        return try Self.decoder.decode(type, from: data)
    }

    private func esc(_ s: String) -> String {
        s.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? s
    }
}
