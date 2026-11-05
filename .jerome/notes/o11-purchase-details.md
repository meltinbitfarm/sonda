# O11: purchase.pending and purchase error detail (D24)

2026-11-05, branch `sdk/purchase-details`. Ported from TeSeFredda `Telemetria.swift` (`ErroreAcquisto`), same prop names and values.

## What changed in the SDK (sdk/Sources/Sonda/Sonda+Catalog.swift)

- `Sonda.Event.purchasePending = "purchase.pending"` and `Sonda.purchasePending(productID:)` -> `{product_id}`.
- `purchaseResult(_:productID:)`: `.pending` -> `purchase.pending` (was purchase.failed error "pending");
  `.success(.unverified(_, e))` -> `purchase.failed {error: "unverified", verification_error, product_id}`;
  `@unknown default` -> `error: "unknownResult"` (was "unknown", which collided with `StoreKitError.unknown`; same as the app).
- New public `Sonda.PurchaseFailure` (Sendable, Equatable): fields `error`, `errorDomain`, `errorCode`,
  `underlyingErrorDomain`, `underlyingErrorCode`, `verificationError`; `init(_ error: any Error)`,
  `init(verificationError:)`, memberwise `init(error:...)`; `props` (nil fields omitted, never `product_id`).
  Mapping identical to the app's `ErroreAcquisto`: StoreKitError / Product.PurchaseError case names, `other`,
  `storeKitOther` / `purchaseErrorOther`; underlying = associated error of networkError/systemError, else NSUnderlyingErrorKey.
- `purchaseFailed(_ error:productID:)` now sends the detail (was `error = "domain:code"`).
  One deliberate difference from a literal port: a thrown `StoreKitError.userCancelled` sends `purchase.cancelled`
  (the app catches it at the call site before building the detail; the helper does it itself so `catch { purchaseFailed }` is right).
  `PurchaseFailure(StoreKitError.userCancelled)` alone still gives `error: "userCancelled"`, like the app.
- `purchaseFailed(_ failure: PurchaseFailure, productID:)` added; `purchaseFailed(error: String, productID:)` kept.
- Two cases newer than the app's switch are named exactly: `StoreKitError.invalidPresentationContext` (iOS 27 SDK)
  and `Product.PurchaseError.paymentMethodBindingConfigurationRequired` (iOS 26.5 SDK), both under
  `#if compiler(>=6.4)` so the package still builds with older Xcodes (there they fall into storeKitOther / purchaseErrorOther).
  The app today sends storeKitOther / purchaseErrorOther for them: this is the only value difference.

## Server

No change needed: `internal/ingest` validates names generically (`area.action` snake_case, <= 64) and props generically
(flat, snake_case keys <= 64, <= 2048 bytes). Worst case PurchaseFailure + 200-char product_id passes `EventRules.validate`
(test `propsPassTheContractRules`). Only the MCP instructions text (`internal/mcpserver/mcpserver.go`) lists the catalogue:
now includes purchase.pending and the purchase.failed props. ingest-contract.md has no catalogue list: unchanged.

## Tests

- New `sdk/Tests/SondaTests/PurchaseTests.swift` (6 tests, same cases as TeSeFredda's `erroreAcquistoDettagliato`, plus no localized message, contract rules).
- `PublicAPITests` covers purchasePending, purchaseFailed(StoreKitError.networkError) and thrown userCancelled.
- `purchaseResult` itself is not unit-tested: `Product.PurchaseResult` needs a real `Transaction` (StoreKit Test would be needed).
- swift test: 53 tests passed. xcodebuild iOS Simulator (iPhone 17, 26.2): 52 passed. Release builds iOS, macOS, Mac Catalyst: succeeded.
- Go: gofmt clean, go vet + go test ./... passed (Docker recipe).

## Docs

sdk/README.md (catalogue table, example, new "Purchases in detail"), README.md (comment line), notes/sdk-client.md item 14,
notes/recipes/sdk-tests.md (test counts). decisions.md D12 text not touched (D24 already extends it).

## TeSeFredda migration (after a Sonda release that includes this; not tagged here)

In `Telemetria.swift`:
- delete `struct ErroreAcquisto` and add `typealias ErroreAcquisto = Sonda.PurchaseFailure`, then rename uses:
  `.parametri` -> `.props`, `ErroreAcquisto(verificaFallita: v)` -> `ErroreAcquisto(verificationError: v)`,
  `ErroreAcquisto(codice: "unknownResult")` -> `ErroreAcquisto(error: "unknownResult")`,
  in Negozio's log line `codice/dominio/numero/dominioSotto/numeroSotto` -> `error/errorDomain/errorCode/underlyingErrorDomain/underlyingErrorCode`.
- `nome`: `case .paywallAcquistoInAttesa: Sonda.Event.purchasePending` (split from `.paywallAcquistoFallito`).
- `parametri`: `.paywallAcquistoInAttesa` joins the started/completed/cancelled case (drop `"error": "pending"`).
- TelemetriaTest: same renames, pending name `purchase.pending`, the "unique names except purchase.failed" comment.
Props of purchase.failed stay byte-for-byte the same, so existing queries keep working.
