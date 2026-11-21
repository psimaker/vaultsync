import Foundation

/// What the yearly plan saves against paying monthly for a year, in whole
/// percent (#352). Pure, so the arithmetic is testable without StoreKit; the
/// plan picker feeds it the two products' prices.
enum PlanSavings {
    /// The saving in whole percent, or nil when there is none to show: no
    /// monthly price, or a yearly price that saves nothing.
    static func yearlyPercent(monthly: Decimal, yearly: Decimal) -> Int? {
        let monthlyAnnual = monthly * 12
        guard monthlyAnnual > 0 else { return nil }
        var exact = (monthlyAnnual - yearly) * 100 / monthlyAnnual
        // Round before converting: `NSDecimalNumber.intValue` reads a
        // `Decimal` that carries 38 significant digits — any quotient that
        // does not terminate, which most pairs of prices give — as 0, and
        // the tag never showed. Down, so the tag never promises more than the plan
        // saves.
        var whole = Decimal()
        NSDecimalRound(&whole, &exact, 0, .down)
        let percent = NSDecimalNumber(decimal: whole).intValue
        guard percent > 0 else { return nil }
        return percent
    }
}
