import Foundation
import StoreKit

/// The standard catalogue (D12, extended by D24): same names and props in every app, so the same
/// funnels and queries work everywhere. `app.first_launch` and `app.session_started`
/// are sent automatically.
extension Sonda {
    /// Event names of the D12 catalogue (plus `purchase.pending`, D24).
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
        public static let purchasePending = "purchase.pending"
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

    /// `purchase.pending {product_id}`: StoreKit 2 `.pending`, e.g. Ask to Buy or a payment that needs
    /// an action outside the app. Not a failure: the purchase may still complete later (D24).
    public static func purchasePending(productID: String) {
        track(Event.purchasePending, props: ["product_id": .string(productID)])
    }

    /// `purchase.failed {error}` (plus `product_id` when known). `error` is a short code
    /// such as `networkError`, never a localized message (it may contain personal data).
    public static func purchaseFailed(error: String, productID: String? = nil) {
        purchaseFailed(PurchaseFailure(error: error), productID: productID)
    }

    /// `purchase.failed` from a thrown error, with the detail of ``PurchaseFailure``:
    /// `error` (exact StoreKit case), `error_domain`, `error_code`, `underlying_error_domain`,
    /// `underlying_error_code`. `StoreKitError.userCancelled` is not a failure: it sends
    /// `purchase.cancelled` (D12), so `catch { Sonda.purchaseFailed(error, ...) }` is always right.
    public static func purchaseFailed(_ error: any Error, productID: String? = nil) {
        if case StoreKitError.userCancelled? = error as? StoreKitError {
            var props: [String: SondaValue] = [:]
            if let productID { props["product_id"] = .string(productID) }
            track(Event.purchaseCancelled, props: props)
            return
        }
        purchaseFailed(PurchaseFailure(error), productID: productID)
    }

    /// `purchase.failed` with a prepared ``PurchaseFailure`` (plus `product_id` when known).
    public static func purchaseFailed(_ failure: PurchaseFailure, productID: String? = nil) {
        var props = failure.props
        if let productID { props["product_id"] = .string(productID) }
        track(Event.purchaseFailed, props: props)
    }

    /// `purchase.restored`
    public static func purchaseRestored() {
        track(Event.purchaseRestored)
    }

    /// Maps a StoreKit 2 purchase result: success verified -> `purchase.completed`,
    /// success unverified -> `purchase.failed {error: "unverified", verification_error}`,
    /// userCancelled -> `purchase.cancelled`, pending -> `purchase.pending`,
    /// a result added by a future StoreKit -> `purchase.failed {error: "unknownResult"}`.
    public static func purchaseResult(_ result: Product.PurchaseResult, productID: String) {
        switch result {
        case .success(.verified):
            purchaseCompleted(productID: productID)
        case .success(.unverified(_, let verificationError)):
            purchaseFailed(PurchaseFailure(verificationError: verificationError), productID: productID)
        case .userCancelled:
            purchaseCancelled(productID: productID)
        case .pending:
            purchasePending(productID: productID)
        @unknown default:
            purchaseFailed(PurchaseFailure(error: "unknownResult"), productID: productID)
        }
    }

    /// The detail of a failed purchase, as sent in `purchase.failed` (D24). Flat props, never a
    /// localized message (D11):
    /// - `error`: the exact case, short and low-cardinality: the `StoreKitError` or
    ///   `Product.PurchaseError` case name, `unverified` (StoreKit verification failed, the reason in
    ///   `verification_error`), `unknownResult` (a new `purchase()` result), otherwise `other`;
    /// - `error_domain`, `error_code`: the NSError domain and code of the thrown error;
    /// - `underlying_error_domain`, `underlying_error_code`: the cause (the error carried by
    ///   `networkError` and `systemError`, otherwise `NSUnderlyingErrorKey`), useful to read an `unknown`;
    /// - `verification_error`: the `VerificationResult.VerificationError` case for `unverified`.
    ///
    /// Use it directly to add app-specific props: `Sonda.track(Sonda.Event.purchaseFailed,
    /// props: failure.props.merging(["placement": "onboarding"]) { a, _ in a })`.
    public struct PurchaseFailure: Sendable, Equatable {
        public var error: String
        public var errorDomain: String?
        public var errorCode: Int?
        public var underlyingErrorDomain: String?
        public var underlyingErrorCode: Int?
        public var verificationError: String?

        public init(error: String, errorDomain: String? = nil, errorCode: Int? = nil,
                    underlyingErrorDomain: String? = nil, underlyingErrorCode: Int? = nil,
                    verificationError: String? = nil) {
            self.error = error
            self.errorDomain = errorDomain
            self.errorCode = errorCode
            self.underlyingErrorDomain = underlyingErrorDomain
            self.underlyingErrorCode = underlyingErrorCode
            self.verificationError = verificationError
        }

        /// From an error thrown by `Product.purchase()` (or any error: `error` is then `other`).
        public init(_ thrown: any Error) {
            let ns = thrown as NSError
            var underlying = ns.userInfo[NSUnderlyingErrorKey] as? NSError
            let code: String
            if let e = thrown as? StoreKitError {
                switch e {
                case .unknown: code = "unknown"
                case .userCancelled: code = "userCancelled"
                case .networkError(let u): code = "networkError"; underlying = u as NSError
                case .systemError(let s): code = "systemError"; underlying = s as NSError
                case .notAvailableInStorefront: code = "notAvailableInStorefront"
                case .notEntitled: code = "notEntitled"
                case .unsupported: code = "unsupported"
                #if compiler(>=6.4)   // iOS 27 SDK (Xcode 27); older Xcodes fall into storeKitOther
                case .invalidPresentationContext: code = "invalidPresentationContext"
                #endif
                @unknown default: code = "storeKitOther"
                }
            } else if let e = thrown as? Product.PurchaseError {
                switch e {
                case .invalidQuantity: code = "invalidQuantity"
                case .productUnavailable: code = "productUnavailable"
                case .purchaseNotAllowed: code = "purchaseNotAllowed"
                case .ineligibleForOffer: code = "ineligibleForOffer"
                case .invalidOfferIdentifier: code = "invalidOfferIdentifier"
                case .invalidOfferPrice: code = "invalidOfferPrice"
                case .invalidOfferSignature: code = "invalidOfferSignature"
                case .missingOfferParameters: code = "missingOfferParameters"
                #if compiler(>=6.4)   // iOS 26.5 SDK; guarded with Xcode 27 to keep older Xcodes building
                case .paymentMethodBindingConfigurationRequired: code = "paymentMethodBindingConfigurationRequired"
                #endif
                @unknown default: code = "purchaseErrorOther"
                }
            } else {
                code = "other"
            }
            self.init(error: code, errorDomain: ns.domain, errorCode: ns.code,
                      underlyingErrorDomain: underlying?.domain, underlyingErrorCode: underlying?.code)
        }

        /// `.success(.unverified)`: StoreKit returned the transaction but its signature does not check out.
        public init(verificationError: VerificationResult<StoreKit.Transaction>.VerificationError) {
            let reason: String
            switch verificationError {
            case .revokedCertificate: reason = "revokedCertificate"
            case .invalidCertificateChain: reason = "invalidCertificateChain"
            case .invalidDeviceVerification: reason = "invalidDeviceVerification"
            case .invalidEncoding: reason = "invalidEncoding"
            case .invalidSignature: reason = "invalidSignature"
            case .missingRequiredProperties: reason = "missingRequiredProperties"
            @unknown default: reason = "other"
            }
            self.init(error: "unverified", verificationError: reason)
        }

        /// The props of `purchase.failed`, without `product_id`; nil fields are omitted.
        public var props: [String: SondaValue] {
            var p: [String: SondaValue] = ["error": .string(error)]
            if let errorDomain { p["error_domain"] = .string(errorDomain) }
            if let errorCode { p["error_code"] = .int(errorCode) }
            if let underlyingErrorDomain { p["underlying_error_domain"] = .string(underlyingErrorDomain) }
            if let underlyingErrorCode { p["underlying_error_code"] = .int(underlyingErrorCode) }
            if let verificationError { p["verification_error"] = .string(verificationError) }
            return p
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
