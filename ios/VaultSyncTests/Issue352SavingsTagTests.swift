import Foundation
import Testing
@testable import VaultSync

/// #352: the yearly plan's "Save N%" tag never appeared. The saving was read
/// with `NSDecimalNumber(decimal:).intValue`, which gives 0 for a quotient
/// that does not terminate (a `Decimal` with 38 significant digits), which
/// is what most pairs of prices give. The tag rounds down, so it never
/// promises more than the plan saves.
@Suite("The yearly plan's savings tag (#352)")
struct Issue352SavingsTagTests {
    @Test("A saving whose quotient does not terminate is shown, rounded down")
    func nonTerminatingQuotientIsShown() {
        // 12 × 7 = 84; (84 − 50) / 84 = 40.476…%
        #expect(PlanSavings.yearlyPercent(monthly: 7, yearly: 50) == 40)
        // 12 × 1.99 = 23.88; (23.88 − 14.99) / 23.88 = 37.227…%
        #expect(PlanSavings.yearlyPercent(monthly: Decimal(string: "1.99")!, yearly: Decimal(string: "14.99")!) == 37)
    }

    @Test("The tag never rounds up")
    func roundsDown() {
        // 12 × 1 = 12; (12 − 6.01) / 12 = 49.916…% — shown as 49, never 50.
        #expect(PlanSavings.yearlyPercent(monthly: 1, yearly: Decimal(string: "6.01")!) == 49)
    }

    @Test("A saving that terminates still shows")
    func terminatingQuotient() {
        #expect(PlanSavings.yearlyPercent(monthly: 10, yearly: 60) == 50)
    }

    @Test("No saving, no tag")
    func noSavingNoTag() {
        #expect(PlanSavings.yearlyPercent(monthly: 1, yearly: 12) == nil)
        #expect(PlanSavings.yearlyPercent(monthly: 1, yearly: 13) == nil)
        // Less than one whole percent rounds down to nothing.
        #expect(PlanSavings.yearlyPercent(monthly: 1, yearly: Decimal(string: "11.95")!) == nil)
        #expect(PlanSavings.yearlyPercent(monthly: 0, yearly: 10) == nil)
    }
}
