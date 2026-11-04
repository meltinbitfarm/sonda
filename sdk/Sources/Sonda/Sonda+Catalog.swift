import Foundation
import StoreKit

/// The standard catalogue (D12): same names and props in every app, so the same
/// funnels and queries work everywhere. `app.first_launch` and `app.session_started`
/// are sent automatically.
extension Sonda {
    /// Event names of the D12 catalogue.
    public enum Event {
        public static let firstLaunch = "app.first_launch"
        public static let sessionStarted = "app.session_started"
        public static let onboardingStepViewed = "onboarding.step_viewed"
        public static let onboardingCompleted = "onboarding.completed"
        public static let onboardingSkipped = "onboarding.skipped"
        public static let screenViewed = "screen.viewed"
        public static let featureUsed = "feature.used"
        public static let paywallShown = "paywall.shown"
        public static let purchaseStarted = "purchase.started"
        public static let purchaseCompleted = "purchase.completed"
        public static let purchaseCancelled = "purchase.cancelled"
        public static let purchaseFailed = "purchase.failed"
        public static let purchaseRestored = "purchase.restored"
        public static let errorOccurred = "error.occurred"
    }

    // MARK: Onboarding

    /// `onboarding.step_viewed {step, step_name}`; `step` counts from 1.
    public static func onboardingStepViewed(step: Int, name: String) {
        track(Event.onboardingStepViewed, props: ["step": .int(step), "step_name": .string(name)])
    }

    /// `onboarding.completed`
    public static func onboardingCompleted() {
        track(Event.onboardingCompleted)
    }

    /// `onboarding.skipped {at_step}`
    public static func onboardingSkipped(atStep step: Int) {
        track(Event.onboardingSkipped, props: ["at_step": .int(step)])
    }

    // MARK: Usage

    /// `screen.viewed {screen}`
    public static func screenViewed(_ screen: String) {
        track(Event.screenViewed, props: ["screen": .string(screen)])
    }

    /// `feature.used {feature}`
    public static func featureUsed(_ feature: String) {
        track(Event.featureUsed, props: ["feature": .string(feature)])
    }

    // MARK: Paywall and purchases

    /// `paywall.shown {placement, variant}`; `variant` omitted when nil.
    public static func paywallShown(placement: String, variant: String? = nil) {
        var props: [String: SondaValue] = ["placement": .string(placement)]
        if let variant { props["variant"] = .string(variant) }
        track(Event.paywallShown, props: props)
    }

    /// `purchase.started {product_id}`
    public static func purchaseStarted(productID: String) {
        track(Event.purchaseStarted, props: ["product_id": .string(productID)])
    }

    /// `purchase.completed {product_id}`
    public static func purchaseCompleted(productID: String) {
        track(Event.purchaseCompleted, props: ["product_id": .string(productID)])
    }

    /// `purchase.cancelled {product_id}`: StoreKit 2 `.userCancelled`.
    public static func purchaseCancelled(productID: String) {
        track(Event.purchaseCancelled, props: ["product_id": .string(productID)])
    }

    /// `purchase.failed {error}` (plus `product_id` when known). `error` is a short code
    /// such as `network` or `SKErrorDomain:2`, never a localized message (it may contain personal data).
    public static func purchaseFailed(error: String, productID: String? = nil) {
        var props: [String: SondaValue] = ["error": .string(error)]
        if let productID { props["product_id"] = .string(productID) }
        track(Event.purchaseFailed, props: props)
    }

    /// `purchase.failed` from a thrown error, as `domain:code`.
    public static func purchaseFailed(_ error: any Error, productID: String? = nil) {
        let ns = error as NSError
        purchaseFailed(error: "\(ns.domain):\(ns.code)", productID: productID)
    }

    /// `purchase.restored`
    public static func purchaseRestored() {
        track(Event.purchaseRestored)
    }

    /// Maps a StoreKit 2 purchase result: success -> `purchase.completed`,
    /// userCancelled -> `purchase.cancelled`, pending -> `purchase.failed {error: "pending"}`.
    public static func purchaseResult(_ result: Product.PurchaseResult, productID: String) {
        switch result {
        case .success(.verified):
            purchaseCompleted(productID: productID)
        case .success(.unverified):
            purchaseFailed(error: "unverified", productID: productID)
        case .userCancelled:
            purchaseCancelled(productID: productID)
        case .pending:
            purchaseFailed(error: "pending", productID: productID)
        @unknown default:
            purchaseFailed(error: "unknown", productID: productID)
        }
    }

    // MARK: Errors

    /// `error.occurred {domain, code}`
    public static func errorOccurred(domain: String, code: Int) {
        track(Event.errorOccurred, props: ["domain": .string(domain), "code": .int(code)])
    }

    /// `error.occurred` from a thrown error (NSError domain and code; never the message).
    public static func errorOccurred(_ error: any Error) {
        let ns = error as NSError
        errorOccurred(domain: ns.domain, code: ns.code)
    }
}
