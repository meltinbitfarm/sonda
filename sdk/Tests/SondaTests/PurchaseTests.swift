import Foundation
import StoreKit
import Testing
@testable import Sonda

/// `purchase.failed` detail (D24): same props and values as TeSeFredda's `ErroreAcquisto`.
@Suite struct PurchaseFailureTests {
    @Test func storeKitNetworkErrorCarriesTheURLErrorUnderneath() {
        let f = Sonda.PurchaseFailure(StoreKitError.networkError(URLError(.notConnectedToInternet)))
        #expect(f.error == "networkError")
        #expect(f.errorDomain != nil && f.errorCode != nil)
        #expect(f.underlyingErrorDomain == NSURLErrorDomain)
        #expect(f.underlyingErrorCode == URLError.notConnectedToInternet.rawValue)
        #expect(f.props["underlying_error_domain"] == .string(NSURLErrorDomain))
        #expect(f.props["underlying_error_code"] == .int(URLError.notConnectedToInternet.rawValue))
        #expect(f.props["error_domain"] != nil && f.props["error_code"] != nil)
        #expect(f.verificationError == nil)
    }

    @Test func exactStoreKitCaseNames() {
        #expect(Sonda.PurchaseFailure(StoreKitError.unknown).error == "unknown")
        #expect(Sonda.PurchaseFailure(StoreKitError.userCancelled).error == "userCancelled")
        #expect(Sonda.PurchaseFailure(StoreKitError.notEntitled).error == "notEntitled")
        #expect(Sonda.PurchaseFailure(StoreKitError.notAvailableInStorefront).error == "notAvailableInStorefront")
        if #available(iOS 18.4, macOS 15.4, *) {
            #expect(Sonda.PurchaseFailure(StoreKitError.unsupported).error == "unsupported")
        }
        let system = Sonda.PurchaseFailure(StoreKitError.systemError(NSError(domain: "ASDErrorDomain", code: 500)))
        #expect(system.error == "systemError")
        #expect(system.underlyingErrorDomain == "ASDErrorDomain" && system.underlyingErrorCode == 500)
        #expect(Sonda.PurchaseFailure(Product.PurchaseError.invalidOfferSignature).error == "invalidOfferSignature")
        #expect(Sonda.PurchaseFailure(Product.PurchaseError.ineligibleForOffer).error == "ineligibleForOffer")
        #expect(Sonda.PurchaseFailure(Product.PurchaseError.purchaseNotAllowed).error == "purchaseNotAllowed")
    }

    @Test func anyOtherErrorIsOtherWithDomainCodeAndUnderlyingKey() {
        let under = NSError(domain: "SKErrorDomain", code: 0)
        let f = Sonda.PurchaseFailure(NSError(domain: "AMSErrorDomain", code: 305, userInfo: [
            NSUnderlyingErrorKey: under, NSLocalizedDescriptionKey: "Mario Rossi, la carta non è valida",
        ]))
        // Exactly these props: never the localized message (D11).
        #expect(f.props == ["error": "other", "error_domain": "AMSErrorDomain", "error_code": 305,
                            "underlying_error_domain": "SKErrorDomain", "underlying_error_code": 0])
    }

    @Test func unverifiedCarriesTheVerificationReasonOnly() {
        let f = Sonda.PurchaseFailure(verificationError: .invalidSignature)
        #expect(f.props == ["error": "unverified", "verification_error": "invalidSignature"])
        #expect(Sonda.PurchaseFailure(verificationError: .revokedCertificate).verificationError == "revokedCertificate")
        #expect(Sonda.PurchaseFailure(verificationError: .missingRequiredProperties).verificationError == "missingRequiredProperties")
    }

    @Test func plainCodeHasOnlyError() {
        #expect(Sonda.PurchaseFailure(error: "unknownResult").props == ["error": "unknownResult"])
    }

    @Test func propsPassTheContractRules() {
        let worst = Sonda.PurchaseFailure(error: "paymentMethodBindingConfigurationRequired",
                                          errorDomain: "StoreKit.Product.PurchaseError", errorCode: Int.min,
                                          underlyingErrorDomain: "com.apple.commerce.client.some.long.domain", underlyingErrorCode: Int.min,
                                          verificationError: "missingRequiredProperties")
        var props = worst.props
        props["product_id"] = .string(String(repeating: "p", count: 200))
        #expect(EventRules.validate(props: props) == nil)
        for key in props.keys { #expect(EventRules.isValidPropKey(key)) }
        #expect(EventRules.isValidName(Sonda.Event.purchasePending))
    }
}
