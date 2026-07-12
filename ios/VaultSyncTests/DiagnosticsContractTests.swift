import CryptoKit
import Foundation
import Testing
@testable import VaultSync

@Suite("Diagnostics contract test-only foundation (M1)")
struct DiagnosticsContractTests {
    @Test("Canonical fixture covers Decisions 022-024 and exact registries")
    func fixtureCatalogs() throws {
        let fixture = try M1ContractFixtureLoader.load()
        #expect(fixture.fixtureVersion == 1)
        #expect(fixture.sourceDecisions == ["022", "023", "024"])
        #expect(fixture.limits == .init(
            maximumMessageBytes: 16_384,
            maximumMapEntries: 32,
            maximumArrayEntries: 8,
            maximumNestingDepth: 4
        ))
        #expect(Set(fixture.registries.keys) == ["pairing_bootstrap", "pairing_lifecycle", "namespace", "roundtrip"])
        #expect(fixture.registries["pairing_bootstrap"]?.count == 27)
        #expect(fixture.registries["pairing_lifecycle"]?.count == 30)
        #expect(fixture.registries["namespace"]?.count == 34)
        #expect(fixture.registries["roundtrip"]?.count == 31)
        #expect(fixture.registries["roundtrip"]?["26"] == nil)
        #expect(fixture.registries["roundtrip"]?["255"] == "signature:bstr=64")
        #expect(fixture.domains.count == 41)
        #expect(fixture.digestChains.count == 27)
        #expect(fixture.domains.values.allSatisfy { $0.last == "\0" && $0.dropLast().allSatisfy { $0 != "\0" } })
    }

    @Test("CryptoKit verifies the byte-exact RFC 8032 vector and signs validly")
    func rfc8032() throws {
        let fixture = try M1ContractFixtureLoader.load()
        let vectors = [fixture.vectors.rfc8032] + fixture.vectors.rfc8032Additional
        #expect(vectors.count >= 2)
        for vector in vectors {
            let privateKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(m1Hex: vector.seedHex))
            let message = try Data(m1Hex: vector.messageHex)
            let generatedSignature = try privateKey.signature(for: message)
            let vectorSignature = try Data(m1Hex: vector.signatureHex)
            #expect(privateKey.publicKey.rawRepresentation.m1Hex == vector.publicKeyHex)
            #expect(privateKey.publicKey.isValidSignature(vectorSignature, for: message))
            #expect(privateKey.publicKey.isValidSignature(generatedSignature, for: message))
        }
    }

    @Test("Decision 022 normative bootstrap HMAC is byte-exact")
    func bootstrapHMAC() throws {
        let vector = try M1ContractFixtureLoader.load().vectors.bootstrapHmac
        let body = try Data(m1Hex: vector.canonicalBodyHex)
        _ = try M1DeterministicCBOR.decode(body)
        let result = M1ContractCrypto.hmacSHA256(
            key: try Data(m1Hex: vector.secretHex),
            domain: "eu.vaultsync.helper-pairing/v1/bootstrap-hmac\0",
            body: body
        )
        #expect(result.m1Hex == vector.expectedHmacHex)
    }

    @Test("Go and Swift reproduce the same derivations and canonical query")
    func crossLanguageGoldens() throws {
        let fixture = try M1ContractFixtureLoader.load()
        let vector = fixture.vectors.derivations
        let publicKey = try Data(m1Hex: vector.rawPublicKeyHex)
        let keyID = M1ContractCrypto.keyID(publicKey: publicKey)
        #expect(keyID.m1Hex == vector.expectedKeyIdHex)

        let deviceDigest = M1ContractCrypto.sha256(
            domain: "eu.vaultsync.binding/syncthing-device/v1\0",
            body: try Data(m1Hex: vector.rawDeviceIdHex)
        )
        #expect(deviceDigest.m1Hex == vector.expectedDeviceIdDigestHex)

        var folderInput = Data()
        let folderBytes = Data(vector.folderId.utf8)
        let length = UInt32(folderBytes.count).bigEndian
        Swift.withUnsafeBytes(of: length) { folderInput.append(contentsOf: $0) }
        folderInput.append(folderBytes)
        let folderDigest = M1ContractCrypto.sha256(
            domain: "eu.vaultsync.binding/syncthing-folder/v1\0",
            body: folderInput
        )
        #expect(folderDigest.m1Hex == vector.expectedFolderIdDigestHex)

        let spkiPin = M1ContractCrypto.sha256(try Data(m1Hex: vector.tlsSpkiDerHex))
        #expect(spkiPin.m1Hex == vector.expectedTlsSpkiPinHex)

        var installationInput = keyID
        installationInput.append(try Data(m1Hex: vector.homeserverBindingHex))
        installationInput.append(try Data(m1Hex: vector.folderBindingHex))
        let installationBinding = M1ContractCrypto.sha256(
            domain: "eu.vaultsync.namespace/installation/v1\0",
            body: installationInput
        )
        #expect(installationBinding.m1Hex == vector.expectedInstallationBindingHex)
        #expect(M1ContractCrypto.base32LowerNoPadding(installationBinding) == vector.expectedInstallationComponent)

        var fingerprintInput = try Data(m1Hex: vector.appRequestDigestHex)
        fingerprintInput.append(try Data(m1Hex: vector.helperAcceptDigestHex))
        let fingerprint = M1ContractCrypto.sha256(
            domain: "eu.vaultsync.helper-pairing/v1/transcript-fingerprint\0",
            body: fingerprintInput
        ).prefix(6).m1Hex.uppercased()
        #expect(fingerprint == vector.expectedTranscriptFingerprint)

        let operationComponent = M1ContractCrypto.base32LowerNoPadding(try Data(m1Hex: vector.operationIdHex))
        #expect(operationComponent == vector.expectedOperationComponent)
        #expect(operationComponent + ".request.cbor" == vector.expectedRequestFilename)
        #expect(operationComponent + ".attestation.cbor" == vector.expectedAttestationFilename)
        #expect(operationComponent + ".response.cbor" == vector.expectedResponseFilename)
        #expect("1.helper-manifest.cbor" == vector.expectedHelperEpochFilename)
        #expect("1.authorization.cbor" == vector.expectedAuthorizationEpochFilename)

        let payload = Data((0...255).map(UInt8.init))
        #expect(M1ContractCrypto.sha256(payload).m1Hex == vector.expectedPayloadDigestHex)

        let query = try M1ContractQueryGolden.make(fixture: fixture)
        #expect(query.body.m1Hex == fixture.vectors.contractQuery.expectedCanonicalBodyHex)
        #expect(query.digest.m1Hex == fixture.vectors.contractQuery.expectedDigestHex)
        let decoded = try M1DeterministicCBOR.decode(query.body)
        try m1ValidateCapabilityQuery(decoded, fixture: fixture)
        var signedInput = Data(try #require(fixture.domains["roundtrip.capability_query"]).utf8)
        signedInput.append(query.body)
        let publicSigningKey = try Curve25519.Signing.PublicKey(rawRepresentation: Data(m1Hex: fixture.vectors.rfc8032.publicKeyHex))
        let crossLanguageSignature = try Data(m1Hex: fixture.vectors.contractQuery.expectedSignatureHex)
        #expect(publicSigningKey.isValidSignature(crossLanguageSignature, for: signedInput))
        #expect(publicSigningKey.isValidSignature(query.signature, for: signedInput))
    }

    @Test("Every Decision 022-024 signature domain has a distinct golden digest")
    func allSignatureDomains() throws {
        let fixture = try M1ContractFixtureLoader.load()
        #expect(fixture.domainBodyDigests.count == fixture.domains.count)
        var seen: Set<String> = []
        for (name, domain) in fixture.domains {
            let digest = M1ContractCrypto.sha256(domain: domain, body: Data([0xa0])).m1Hex
            #expect(digest == fixture.domainBodyDigests[name])
            #expect(seen.insert(digest).inserted)
        }
    }

    @Test("Parser rejects duplicate, reordered, non-shortest, indefinite, tag, float and deep input")
    func parserRejections() throws {
        let invalidHex = [
            "a201000101", "a202000100", "a1616100", "1817", "580100",
            "5f4100ff", "9f00ff", "bf0100ff", "20", "c000", "f90000",
            "f4", "f6", "1c", "5a00010000",
        ]
        for value in invalidHex {
            expectM1Rejected(try Data(m1Hex: value))
        }
        expectM1Rejected(Data(repeating: 0, count: M1DeterministicCBOR.maximumMessageBytes + 1))
        expectM1Rejected(Data(repeating: 0x81, count: M1DeterministicCBOR.maximumNestingDepth + 2) + Data([0]))
    }

    @Test("Parser rejects every truncation, trailing input and invalid schema field")
    func truncationTrailingAndSchema() throws {
        let fixture = try M1ContractFixtureLoader.load()
        let golden = try Data(m1Hex: fixture.vectors.contractQuery.expectedCanonicalBodyHex)
        for length in 0..<golden.count {
            expectM1Rejected(golden.prefix(length))
        }
        expectM1Rejected(golden + Data([0]))

        let decoded = try M1DeterministicCBOR.decode(golden)
        let mutations: [(UInt64, M1CBORValue)] = [
            (1, .bytes(Data(fixture.capabilities["roundtrip", default: ""].utf8))),
            (1, .text("eu.vaultsync.diagnostics.correlated-roundtrip/2")),
            (2, .unsigned(2)),
            (5, .bytes(Data(repeating: 0, count: 31))),
            (7, .bytes(Data(repeating: 0, count: 33))),
            (12, .text("1700000000")),
            (13, .unsigned(fixture.vectors.contractQuery.issuedAt)),
        ]
        for (label, replacement) in mutations {
            let candidate = m1Replacing(decoded, label: label, value: replacement)
            let canonical = try M1DeterministicCBOR.encode(candidate)
            let parsed = try M1DeterministicCBOR.decode(canonical)
            expectM1SchemaRejected(parsed, fixture: fixture)
        }
        guard case .map(var fields) = decoded else { return }
        fields.append(M1CBORField(label: 26, value: .unsigned(1)))
        let unknown = try M1DeterministicCBOR.decode(M1DeterministicCBOR.encode(.map(fields)))
        expectM1SchemaRejected(unknown, fixture: fixture)
    }

    @Test("Canonical model roundtrips and arbitrary bytes never bypass re-encoding")
    func canonicalProperty() throws {
        let fixture = try M1ContractFixtureLoader.load()
        let seeds = [
            Data([0x00]), Data([0x17]), Data([0x18, 0x18]), Data([0x40]),
            Data([0x60]), Data([0x80]), Data([0xa0]),
            try Data(m1Hex: fixture.vectors.bootstrapHmac.canonicalBodyHex),
            try Data(m1Hex: fixture.vectors.contractQuery.expectedCanonicalBodyHex),
        ]
        for seed in seeds {
            #expect(try M1DeterministicCBOR.encode(M1DeterministicCBOR.decode(seed)) == seed)
        }

        var random = M1DeterministicRandom(state: 0x0220_2401)
        for _ in 0..<10_000 {
            let length = Int(random.next() % 512)
            let candidate = Data((0..<length).map { _ in UInt8(truncatingIfNeeded: random.next()) })
            guard let value = try? M1DeterministicCBOR.decode(candidate) else { continue }
            #expect(try M1DeterministicCBOR.encode(value) == candidate)
        }
    }

    @Test("State-machine properties preserve causal and terminal evidence boundaries")
    func stateMachineProperties() {
        var machine = M1EvidenceMachine(tuple: "a", operation: "op-a", generation: 1)
        machine.add(tuple: "b", operation: "op-b", generation: 2)
        let tupleBBefore = machine.states["b"]
        machine.apply(.init(tuple: "a", operation: "op-a", generation: 1, event: .download))
        #expect(machine.states["a"]?.download == false)
        for event in [M1EvidenceEvent.upload, .authorize, .freshApply, .download] {
            machine.apply(.init(tuple: "a", operation: "op-a", generation: 1, event: event))
        }
        #expect(machine.states["a"]?.upload == true)
        #expect(machine.states["a"]?.download == true)
        #expect(machine.states["a"]?.roundtrip == true)
        #expect(machine.states["b"] == tupleBBefore)
        let beforeCleanup = machine.states["a"]
        machine.apply(.init(tuple: "a", operation: "op-a", generation: 1, event: .cleanup))
        #expect(machine.states["a"]?.evidenceSnapshot == beforeCleanup?.evidenceSnapshot)

        for terminal in [M1EvidenceEvent.cancel, .timeout, .appRestart, .helperRestart, .engineRestart] {
            var terminalMachine = M1EvidenceMachine(tuple: "a", operation: "op", generation: 1)
            terminalMachine.apply(.init(tuple: "a", operation: "op", generation: 1, event: .upload))
            terminalMachine.apply(.init(tuple: "a", operation: "op", generation: 1, event: terminal))
            let before = terminalMachine.states["a"]
            for late in [M1EvidenceEvent.authorize, .freshApply, .download] {
                terminalMachine.apply(.init(tuple: "a", operation: "op", generation: 1, event: late))
            }
            #expect(terminalMachine.states["a"] == before)
        }

        for weak in M1EvidenceEvent.weakSignals {
            var weakMachine = M1EvidenceMachine(tuple: "a", operation: "op", generation: 1)
            weakMachine.apply(.init(tuple: "a", operation: "op", generation: 1, event: weak))
            #expect(weakMachine.states["a"]?.evidenceSnapshot == [false, false, false])
        }
    }

    @Test("Privacy persistence and Cloud Relay v1 snapshots remain minimal")
    func privacyAndRelayV1() throws {
        let fixture = try M1ContractFixtureLoader.load()
        let log = "event=operation_terminal protocol=1 count=1 duration_ms=25 state=interrupted"
        let persistence = #"{"app_epoch":1,"helper_epoch":1,"state":"paired"}"#
        #expect(log == fixture.vectors.privacy.expectedLogSnapshot)
        #expect(persistence == fixture.vectors.privacy.expectedPersistenceSnapshot)
        for sentinel in fixture.vectors.privacy.sentinels {
            #expect(!log.contains(sentinel))
            #expect(!persistence.contains(sentinel))
        }

        let request = try RelayService.makeStatusRequest(
            baseURL: "https://relay.invalid",
            deviceID: "server-a",
            signedTransaction: "header.payload.signature"
        )
        let body = try #require(request.httpBody)
        let object = try #require(JSONSerialization.jsonObject(with: body) as? [String: String])
        #expect(request.url?.path == fixture.vectors.relayV1.statusPath)
        #expect(Set(object.keys) == Set(fixture.vectors.relayV1.statusRequestFields))
        for forbidden in fixture.vectors.relayV1.forbiddenContractFields {
            #expect(object[forbidden] == nil)
        }
        #expect(fixture.vectors.relayV1.triggerPath == "/api/v1/trigger")
        #expect(fixture.vectors.relayV1.triggerBody == #"{"device_id":"TEST-DEVICE-ID"}"#)
    }
}

private func expectM1Rejected(_ data: some DataProtocol, sourceLocation: SourceLocation = #_sourceLocation) {
    do {
        _ = try M1DeterministicCBOR.decode(Data(data))
        Issue.record("Accepted invalid CBOR: \(Data(data).m1Hex)", sourceLocation: sourceLocation)
    } catch {}
}

private func expectM1SchemaRejected(
    _ value: M1CBORValue,
    fixture: M1ContractFixture,
    sourceLocation: SourceLocation = #_sourceLocation
) {
    do {
        try m1ValidateCapabilityQuery(value, fixture: fixture)
        Issue.record("Accepted invalid capability-query schema", sourceLocation: sourceLocation)
    } catch {}
}

private func m1Replacing(_ input: M1CBORValue, label: UInt64, value: M1CBORValue) -> M1CBORValue {
    guard case .map(var fields) = input else { return input }
    guard let index = fields.firstIndex(where: { $0.label == label }) else { return input }
    fields[index].value = value
    return .map(fields)
}

private struct M1DeterministicRandom {
    var state: UInt64

    mutating func next() -> UInt64 {
        state = state &* 6_364_136_223_846_793_005 &+ 1_442_695_040_888_963_407
        return state
    }
}

private enum M1EvidencePhase: Equatable {
    case checking, completed, cancelled, interrupted, timedOut
}

private enum M1EvidenceEvent: CaseIterable {
    case timestamp, http, relay, apns, scan, index, idle, completion, capability, tombstone
    case upload, authorize, freshApply, download, cleanup
    case cancel, timeout, appRestart, helperRestart, engineRestart

    static let weakSignals: [M1EvidenceEvent] = [
        .timestamp, .http, .relay, .apns, .scan, .index, .idle, .completion, .capability, .tombstone,
    ]
}

private struct M1EvidenceTransition {
    let tuple: String
    let operation: String
    let generation: Int
    let event: M1EvidenceEvent
}

private struct M1EvidenceState: Equatable {
    let operation: String
    let generation: Int
    var phase: M1EvidencePhase = .checking
    var upload = false
    var authorized = false
    var freshApply = false
    var download = false
    var roundtrip = false
    var cleanupAttempts = 0

    var evidenceSnapshot: [Bool] { [upload, download, roundtrip] }

    static func == (lhs: M1EvidenceState, rhs: M1EvidenceState) -> Bool {
        lhs.operation == rhs.operation && lhs.generation == rhs.generation && lhs.phase == rhs.phase &&
            lhs.upload == rhs.upload && lhs.authorized == rhs.authorized && lhs.freshApply == rhs.freshApply &&
            lhs.download == rhs.download && lhs.roundtrip == rhs.roundtrip && lhs.cleanupAttempts == rhs.cleanupAttempts
    }
}

private struct M1EvidenceMachine {
    var states: [String: M1EvidenceState]

    init(tuple: String, operation: String, generation: Int) {
        states = [tuple: M1EvidenceState(operation: operation, generation: generation)]
    }

    mutating func add(tuple: String, operation: String, generation: Int) {
        states[tuple] = M1EvidenceState(operation: operation, generation: generation)
    }

    mutating func apply(_ transition: M1EvidenceTransition) {
        guard var state = states[transition.tuple],
              state.operation == transition.operation,
              state.generation == transition.generation else { return }
        if transition.event == .cleanup {
            state.cleanupAttempts += 1
            states[transition.tuple] = state
            return
        }
        guard state.phase == .checking else { return }
        switch transition.event {
        case .upload:
            state.upload = true
        case .authorize where state.upload:
            state.authorized = true
        case .freshApply where state.authorized:
            state.freshApply = true
        case .download where state.upload && state.authorized && state.freshApply:
            state.download = true
            state.roundtrip = true
            state.phase = .completed
        case .cancel:
            state.phase = .cancelled
        case .timeout:
            state.phase = .timedOut
        case .appRestart, .helperRestart, .engineRestart:
            state.phase = .interrupted
        default:
            break
        }
        states[transition.tuple] = state
    }
}
