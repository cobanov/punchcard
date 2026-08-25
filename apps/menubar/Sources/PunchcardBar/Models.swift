import Foundation

/// The API's project, trimmed to what the menu needs.
struct Project: Codable, Sendable, Identifiable {
    let id: String
    let name: String
    let client: String?
}

/// A work session.
///
/// `seconds` is what the server computed at the moment it answered. The menu
/// bar does NOT use it to draw the running clock — it derives elapsed time from
/// `startedAt` instead, so the display keeps ticking between polls rather than
/// freezing at whatever the last response said.
struct Session: Codable, Sendable {
    let id: String
    let projectID: String
    let note: String
    let startedAt: Date
    let endedAt: Date?
    let seconds: Int
    let running: Bool
    let syncState: String?

    enum CodingKeys: String, CodingKey {
        case id
        case projectID = "project_id"
        case note
        case startedAt = "started_at"
        case endedAt = "ended_at"
        case seconds
        case running
        case syncState = "commit_sync_state"
    }

    /// Elapsed seconds right now, for a running session.
    var elapsed: Int {
        guard running else { return seconds }
        return max(0, Int(Date().timeIntervalSince(startedAt)))
    }
}

struct Commit: Codable, Sendable {
    let sha: String
    let repo: String
    let message: String
}

struct GitHubStatus: Codable, Sendable {
    let connected: Bool
    let login: String?
    let lastError: String?

    enum CodingKeys: String, CodingKey {
        case connected
        case login
        case lastError = "last_error"
    }
}

/// The API's RFC 9457 error body. Its `detail` is written for a person, so it
/// is what the app shows rather than inventing its own wording.
struct APIProblem: Codable, Sendable, Error {
    let status: Int?
    let detail: String?
    let code: String?
}

/// Everything the menu draws, assembled in one place so the UI never has to ask
/// "have the projects loaded yet?" while rendering.
struct BarState: Sendable {
    var signedIn = false
    var current: Session?
    var projects: [Project] = []
    var today: [Session] = []
    var todayCommits: Int = 0
    var github: GitHubStatus?
    var lastError: String?

    func projectName(_ id: String) -> String {
        projects.first { $0.id == id }?.name ?? "—"
    }

    var todaySeconds: Int {
        today.filter { !$0.running }.reduce(0) { $0 + $1.seconds }
    }
}
