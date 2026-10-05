import Foundation

// MARK: - Hub pairing payloads (#174, decision 045)
//
// The Go bridge (`go/bridge/hubpairing.go`) runs the pairing protocol — the
// same client, SPAKE2 and code word list as the `vaultsync-hub` CLI — and
// answers every call with a versioned JSON envelope:
// {"v":1,"ok":true,"data":{…}} or {"v":1,"ok":false,"error":{"kind","message"}}.
// These types decode it. Pure, so they are testable without the bridge.

/// Why a pairing step failed — the bridge's `kind`. Unknown kinds decode as
/// `.other` so a newer bridge never breaks an older parser.
enum HubPairingFailureKind: String, Sendable, Equatable {
    /// Wrong code: the Hub ran the key confirmation and counted the attempt.
    case codeRejected
    /// The other side accepted this iPhone's proof but could not prove its
    /// own — whatever answered does not know the code.
    case authenticationFailed
    case codeExpired
    /// Locked after too many wrong codes.
    case codeLocked
    /// No code is active on the Hub.
    case noCode
    /// The Hub forgot the session (it keeps one for five minutes).
    case sessionExpired
    case rateLimited
    case busy
    /// The Hub address is not a private, loopback or link-local IP.
    case notLocal
    case unreachable
    case incompatible
    case badCode
    case badAddress
    /// No network interface took the discovery probe (no network, or Local
    /// Network access denied).
    case noNetwork
    case noSession
    /// The flow ended or a newer one began; the result belongs to nobody.
    case staleFlow
    case inProgress
    case cancelled
    /// The local engine could not add the Hub as a device.
    case engine
    case unknownVault
    /// The Hub answered but did not share; its reason is the message.
    case hubRefused
    /// The provision request may have reached the Hub — never retried
    /// automatically.
    case outcomeUnknown
    case protocolError = "protocol"
    case other
}

struct HubPairingFailure: Error, Equatable, Sendable {
    let kind: HubPairingFailureKind
    /// Diagnostic text from the bridge. May carry addresses — log as private,
    /// never show it as the explanation (the kind picks the copy).
    let message: String
}

/// One vault in the Hub's catalog.
struct HubVault: Decodable, Equatable, Identifiable, Sendable {
    /// The Hub's folder ID — the same ID the vault gets on this iPhone.
    let id: String
    let label: String
    /// The Hub's local file count. Zero also stands for "could not read", so
    /// the UI shows a count only when it is positive.
    let files: Int64
    /// Devices other than the Hub that share the vault.
    let devices: Int
}

/// A Hub that answered the discovery probe.
struct HubCandidate: Decodable, Equatable, Hashable, Sendable {
    /// ip:port of the pairing service, already checked to be local.
    let address: String
    let name: String
}

/// What a successful handshake learned about the Hub.
struct HubHello: Decodable, Equatable, Sendable {
    let hubName: String
    let hubDeviceID: String
    /// False when the Hub could not list its vaults right now — that is not
    /// an empty Hub.
    let catalogAvailable: Bool
    let vaults: [HubVault]
}

enum HubPairingEnvelope {
    private struct Envelope<Payload: Decodable>: Decodable {
        let v: Int
        let ok: Bool
        let data: Payload?
        let error: Failure?
    }

    private struct Failure: Decodable {
        let kind: String
        let message: String
    }

    /// Decodes one bridge answer. Anything unreadable is a protocol failure,
    /// never a success.
    static func decode<Payload: Decodable>(_ raw: String, as: Payload.Type) -> Result<Payload, HubPairingFailure> {
        guard let data = raw.data(using: .utf8),
              let envelope = try? JSONDecoder().decode(Envelope<Payload>.self, from: data),
              envelope.v == 1 else {
            return .failure(HubPairingFailure(kind: .protocolError, message: "unreadable bridge answer"))
        }
        if envelope.ok, let payload = envelope.data {
            return .success(payload)
        }
        guard let failure = envelope.error else {
            return .failure(HubPairingFailure(kind: .protocolError, message: "bridge answer without data or error"))
        }
        return .failure(HubPairingFailure(
            kind: HubPairingFailureKind(rawValue: failure.kind) ?? .other,
            message: failure.message
        ))
    }
}

struct HubAddressPayload: Decodable, Sendable {
    let address: String
}

struct HubDiscoveryPayload: Decodable, Sendable {
    let hubs: [HubCandidate]
}

struct HubProvisionPayload: Decodable, Sendable {
    let provisioned: HubVault
}
