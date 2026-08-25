import Foundation

/// How the menu bar and the menu render time.
///
/// Two formats, because they answer different questions. The title bar has to
/// stay narrow and is glanced at — it says "roughly how long". The open menu is
/// read deliberately and ticks, so it says exactly.
enum Format {

    /// Title-bar form: `1:42`, or `0:07` under an hour. Narrow on purpose —
    /// every character here costs space every other menu bar item wants.
    ///
    /// Hours are never wrapped at 24. A timer left running over a weekend must
    /// read as `61:20` and look alarming, because it is.
    static func title(_ seconds: Int) -> String {
        let s = max(0, seconds)
        return String(format: "%d:%02d", s / 3600, (s % 3600) / 60)
    }

    /// Menu form: `01:42:07`, ticking.
    static func clock(_ seconds: Int) -> String {
        let s = max(0, seconds)
        return String(format: "%02d:%02d:%02d", s / 3600, (s % 3600) / 60, s % 60)
    }

    /// Totals the way a person says them: `6s 12d`. Nobody bills in seconds.
    static func total(_ seconds: Int) -> String {
        let s = max(0, seconds)
        let h = s / 3600, m = (s % 3600) / 60
        if h > 0 && m > 0 { return "\(h)s \(m)d" }
        if h > 0 { return "\(h)s" }
        return "\(m)d"
    }

    /// Wall-clock time in the user's own zone.
    static func hhmm(_ date: Date) -> String {
        let f = DateFormatter()
        f.dateFormat = "HH:mm"
        return f.string(from: date)
    }

    /// Shortens a string for a menu row, keeping whole characters.
    static func fit(_ s: String, _ max: Int) -> String {
        if s.count <= max { return s }
        if max <= 1 { return String(s.prefix(max)) }
        return String(s.prefix(max - 1)) + "…"
    }
}
