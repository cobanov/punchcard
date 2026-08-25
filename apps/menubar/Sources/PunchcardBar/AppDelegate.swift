import AppKit
import UserNotifications

/// The whole app: a status item, a poll loop, and a menu.
@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {

    /// How often the app asks the server what is going on.
    ///
    /// Twenty seconds is the compromise: a timer started from the CLI or the
    /// web shows up almost at once, and the server sees three requests a minute
    /// from an idle laptop. The right answer is the SSE stream the API already
    /// serves — that is the next version of this file, not a redesign.
    private static let pollInterval: TimeInterval = 20

    /// How often the title redraws while a timer runs. The title shows h:mm, so
    /// a second-by-second redraw would light up the CPU to change nothing.
    private static let titleTick: TimeInterval = 15

    /// A timer running this long is almost certainly forgotten.
    private static let runawayAfter: TimeInterval = 8 * 3600

    private var statusItem: NSStatusItem!
    private var state = BarState()
    private var pollTimer: Timer?
    private var titleTimer: Timer?
    private var menuTimer: Timer?
    /// The menu while it is open. Held on the delegate rather than captured by
    /// the tick closure: NSMenu is not Sendable, and capturing it into a timer
    /// block is a data race the compiler is right to refuse.
    private var openMenu: NSMenu?
    private var warnedAboutRunaway: Set<String> = []
    private var baseURL = API.defaultBaseURL

    // MARK: - Lifecycle

    func applicationDidFinishLaunching(_ notification: Notification) {
        if let override = UserDefaults.standard.string(forKey: "BaseURL"),
           let url = URL(string: override) {
            baseURL = url
        }
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        statusItem.menu = NSMenu()
        statusItem.menu?.delegate = self
        redrawTitle()

        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { _, _ in }

        state.signedIn = Keychain.token() != nil
        Task { await refresh() }

        pollTimer = .scheduledTimer(withTimeInterval: Self.pollInterval, repeats: true) { [weak self] _ in
            Task { @MainActor in await self?.refresh() }
        }
        titleTimer = .scheduledTimer(withTimeInterval: Self.titleTick, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.redrawTitle() }
        }
    }

    // MARK: - Data

    private var api: API? {
        guard let token = Keychain.token() else { return nil }
        return API(baseURL: baseURL, token: token)
    }

    /// Reloads everything the menu shows.
    ///
    /// One failure does not blank the menu: the last good state stays on screen
    /// and the reason goes in the menu's error row. A menu bar item that empties
    /// itself because the wifi dropped is worse than one that is briefly stale.
    private func refresh() async {
        guard let api else {
            state.signedIn = false
            redrawTitle()
            return
        }
        state.signedIn = true
        do {
            async let current = api.current()
            async let projects = api.projects()
            async let github = api.github()

            state.current = try await current
            state.projects = try await projects
            state.github = try await github

            let midnight = Calendar.current.startOfDay(for: Date())
            let today = try await api.sessions(since: midnight)
            state.today = today

            var commits = 0
            for session in today where !session.running {
                commits += (try? await api.commits(sessionID: session.id))?.count ?? 0
            }
            state.todayCommits = commits
            state.lastError = nil
        } catch let problem as APIProblem {
            if problem.status == 401 {
                // The token is gone. Say so plainly and stop pretending to be
                // signed in; every menu action would fail the same way.
                Keychain.delete()
                state.signedIn = false
                state.current = nil
            }
            state.lastError = problem.detail ?? "request failed"
        } catch {
            state.lastError = error.localizedDescription
        }
        redrawTitle()
        checkRunaway()
    }

    // MARK: - Title

    private func redrawTitle() {
        let button = statusItem.button
        let image = NSImage(systemSymbolName: "timer", accessibilityDescription: "punchcard")
        image?.isTemplate = true
        button?.image = image

        guard let running = state.current, running.running else {
            button?.title = ""
            return
        }
        button?.title = " " + Format.title(running.elapsed)
    }

    /// Warns once per session when a timer has been running absurdly long.
    ///
    /// This is the one thing a live timer cannot solve on its own, and the menu
    /// bar is the only client positioned to say it — the server has no way to
    /// interrupt anyone.
    private func checkRunaway() {
        guard let running = state.current, running.running,
              Double(running.elapsed) > Self.runawayAfter,
              !warnedAboutRunaway.contains(running.id) else { return }
        warnedAboutRunaway.insert(running.id)

        let content = UNMutableNotificationContent()
        content.title = "Timer still running"
        content.body = "\(state.projectName(running.projectID)) — \(Format.total(running.elapsed)). Still working?"
        UNUserNotificationCenter.current().add(
            UNNotificationRequest(identifier: running.id, content: content, trigger: nil))
    }

    // MARK: - Menu

    func menuWillOpen(_ menu: NSMenu) {
        openMenu = menu
        rebuild(menu)
        // While the menu is open the clock ticks properly — this is the moment
        // the user is actually reading it.
        menuTimer = .scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            Task { @MainActor in
                guard let self, let menu = self.openMenu else { return }
                self.rebuild(menu)
            }
        }
        Task { await refresh() }
    }

    func menuDidClose(_ menu: NSMenu) {
        menuTimer?.invalidate()
        menuTimer = nil
        openMenu = nil
    }

    private func rebuild(_ menu: NSMenu) {
        menu.removeAllItems()

        guard state.signedIn else {
            menu.addItem(withTitle: "Sign in to punchcard…", action: #selector(signIn), keyEquivalent: "")
                .target = self
            addQuit(to: menu)
            return
        }

        if let running = state.current, running.running {
            let header = NSMenuItem(title: "\(state.projectName(running.projectID)) · \(running.note.isEmpty ? "—" : running.note)",
                                    action: nil, keyEquivalent: "")
            header.isEnabled = false
            menu.addItem(header)

            let clock = NSMenuItem(title: "\(Format.clock(running.elapsed))   started \(Format.hhmm(running.startedAt))",
                                   action: nil, keyEquivalent: "")
            clock.isEnabled = false
            menu.addItem(clock)
            menu.addItem(.separator())

            let stop = NSMenuItem(title: "Stop", action: #selector(stopTimer), keyEquivalent: "s")
            stop.target = self
            menu.addItem(stop)
        } else {
            let idle = NSMenuItem(title: "No timer running", action: nil, keyEquivalent: "")
            idle.isEnabled = false
            menu.addItem(idle)
        }

        // Start submenu — most recently used first, because that is what you
        // want nine times out of ten.
        let start = NSMenuItem(title: "Start", action: nil, keyEquivalent: "")
        let projects = NSMenu()
        for project in orderedProjects() {
            let item = NSMenuItem(title: project.name, action: #selector(startTimer(_:)), keyEquivalent: "")
            item.target = self
            item.representedObject = project.id
            projects.addItem(item)
        }
        if projects.items.isEmpty {
            let none = NSMenuItem(title: "No projects", action: nil, keyEquivalent: "")
            none.isEnabled = false
            projects.addItem(none)
        }
        start.submenu = projects
        menu.addItem(start)

        menu.addItem(.separator())
        addToday(to: menu)

        if let error = state.lastError {
            menu.addItem(.separator())
            let row = NSMenuItem(title: Format.fit(error, 48), action: nil, keyEquivalent: "")
            row.isEnabled = false
            menu.addItem(row)
        }
        if let github = state.github {
            if !github.connected {
                addDisabled("GitHub not connected — commits will not attach", to: menu)
            } else if let why = github.lastError, !why.isEmpty {
                addDisabled(Format.fit("GitHub: \(why)", 48), to: menu)
            }
        }

        menu.addItem(.separator())
        let open = NSMenuItem(title: "Open punchcard…", action: #selector(openSite), keyEquivalent: "")
        open.target = self
        menu.addItem(open)
        let out = NSMenuItem(title: "Sign out", action: #selector(signOut), keyEquivalent: "")
        out.target = self
        menu.addItem(out)
        addQuit(to: menu)
    }

    private func addToday(to menu: NSMenu) {
        let finished = state.today.filter { !$0.running }
        let title = finished.isEmpty
            ? "Today — nothing yet"
            : "Today  \(Format.total(state.todaySeconds))   \(finished.count) sessions · \(state.todayCommits) commits"
        addDisabled(title, to: menu)

        for session in finished.prefix(5).reversed() {
            let span = "\(Format.hhmm(session.startedAt))–\(session.endedAt.map(Format.hhmm) ?? "…")"
            let row = "   \(span)  \(Format.fit(state.projectName(session.projectID), 12))  \(Format.fit(session.note.isEmpty ? "—" : session.note, 24))"
            addDisabled(row, to: menu)
        }
    }

    private func addDisabled(_ title: String, to menu: NSMenu) {
        let item = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        item.isEnabled = false
        menu.addItem(item)
    }

    private func addQuit(to menu: NSMenu) {
        menu.addItem(.separator())
        let quit = NSMenuItem(title: "Quit punchcard", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        menu.addItem(quit)
    }

    /// Projects with the most recently used ones first.
    private func orderedProjects() -> [Project] {
        var recency: [String: Date] = [:]
        for session in state.today {
            let existing = recency[session.projectID] ?? .distantPast
            recency[session.projectID] = max(existing, session.startedAt)
        }
        return state.projects.sorted { a, b in
            switch (recency[a.id], recency[b.id]) {
            case let (x?, y?): return x > y
            case (_?, nil): return true
            case (nil, _?): return false
            default: return a.name.localizedCaseInsensitiveCompare(b.name) == .orderedAscending
            }
        }
    }

    // MARK: - Actions

    @objc private func signIn() {
        Task {
            do {
                let token = try await Login.run(baseURL: baseURL)
                Keychain.save(token: token)
                state.signedIn = true
                await refresh()
            } catch {
                state.lastError = error.localizedDescription
                redrawTitle()
            }
        }
    }

    @objc private func signOut() {
        Keychain.delete()
        state = BarState()
        redrawTitle()
    }

    @objc private func startTimer(_ sender: NSMenuItem) {
        guard let projectID = sender.representedObject as? String, let api else { return }
        Task {
            do {
                // No note from the menu bar: asking for one behind a modal is
                // exactly the friction this app removes. Add it from the CLI or
                // correct it later.
                state.current = try await api.start(projectID: projectID, note: "")
                redrawTitle()
                await refresh()
            } catch {
                state.lastError = (error as? APIProblem)?.detail ?? error.localizedDescription
            }
        }
    }

    @objc private func stopTimer() {
        guard let running = state.current, let api else { return }
        Task {
            do {
                _ = try await api.stop(sessionID: running.id)
                state.current = nil
                redrawTitle()
                await refresh()
            } catch {
                state.lastError = (error as? APIProblem)?.detail ?? error.localizedDescription
            }
        }
    }

    @objc private func openSite() {
        NSWorkspace.shared.open(baseURL)
    }
}
