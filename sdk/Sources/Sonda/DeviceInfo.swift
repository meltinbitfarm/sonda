import Foundation

/// Fields that describe the app and device (D10). Nothing here identifies the person.
struct DeviceInfo: Sendable, Equatable {
    var appVersion: String?
    var build: String?
    var osVersion: String?
    var deviceModel: String?
    /// `phone`, `pad` or `mac` (the only values the server accepts).
    var deviceType: String?

    static func current(bundle: Bundle = .main) -> DeviceInfo {
        let model = hardwareModel()
        return DeviceInfo(
            appVersion: bundle.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String,
            build: bundle.object(forInfoDictionaryKey: "CFBundleVersion") as? String,
            osVersion: osVersionString(ProcessInfo.processInfo.operatingSystemVersion),
            deviceModel: model,
            deviceType: deviceType(forModel: model)
        )
    }

    /// `26.1`, or `26.1.2` when there is a patch number.
    static func osVersionString(_ v: OperatingSystemVersion) -> String {
        v.patchVersion > 0
            ? "\(v.majorVersion).\(v.minorVersion).\(v.patchVersion)"
            : "\(v.majorVersion).\(v.minorVersion)"
    }

    /// Raw hardware identifier, e.g. `iPhone16,2`, `iPad14,1`, `Mac14,2`.
    static func hardwareModel() -> String? {
        #if targetEnvironment(simulator)
        // utsname reports the host CPU (arm64) in the simulator.
        return ProcessInfo.processInfo.environment["SIMULATOR_MODEL_IDENTIFIER"]
        #elseif os(macOS) || targetEnvironment(macCatalyst)
        return sysctlString("hw.model")
        #else
        if ProcessInfo.processInfo.isiOSAppOnMac { return sysctlString("hw.model") }
        var info = utsname()
        uname(&info)
        let machine = withUnsafeBytes(of: &info.machine) { raw in
            String(decoding: raw.prefix(while: { $0 != 0 }), as: UTF8.self)
        }
        return machine.isEmpty ? nil : machine
        #endif
    }

    static func deviceType(forModel model: String?) -> String? {
        #if os(macOS) || targetEnvironment(macCatalyst)
        return "mac"
        #else
        if ProcessInfo.processInfo.isiOSAppOnMac { return "mac" }
        guard let model else { return nil }
        if model.hasPrefix("iPad") { return "pad" }
        if model.hasPrefix("iPhone") || model.hasPrefix("iPod") { return "phone" }
        return nil
        #endif
    }

    private static func sysctlString(_ name: String) -> String? {
        var size = 0
        guard sysctlbyname(name, nil, &size, nil, 0) == 0, size > 0 else { return nil }
        var buffer = [CChar](repeating: 0, count: size)
        guard sysctlbyname(name, &buffer, &size, nil, 0) == 0 else { return nil }
        let bytes = buffer.prefix(while: { $0 != 0 }).map { UInt8(bitPattern: $0) }
        let s = String(decoding: bytes, as: UTF8.self)
        return s.isEmpty ? nil : s
    }
}
