import Foundation
import Testing
@testable import PunchcardBar

// The title is glanced at and the clock is read. They format differently on
// purpose, and both have to survive a timer left running overnight.
@Test func titleIsNarrowAndDoesNotWrapAtADay() {
    #expect(Format.title(0) == "0:00")
    #expect(Format.title(6127) == "1:42")
    #expect(Format.title(90 * 3600) == "90:00")
}

@Test func clockTicksInSeconds() {
    #expect(Format.clock(6127) == "01:42:07")
    #expect(Format.clock(59) == "00:00:59")
}

@Test func totalsReadTheWayPeopleSayThem() {
    #expect(Format.total(22320) == "6s 12d")
    #expect(Format.total(3600) == "1s")
    #expect(Format.total(540) == "9d")
    #expect(Format.total(0) == "0d")
}

@Test func negativeDurationsDoNotProduceNonsense() {
    #expect(Format.title(-5) == "0:00")
    #expect(Format.clock(-5) == "00:00:00")
}

@Test func fitKeepsWholeCharacters() {
    #expect(Format.fit("capsarsiv", 20) == "capsarsiv")
    #expect(Format.fit("yorum sistemi refactor", 10) == "yorum sis…")
}
