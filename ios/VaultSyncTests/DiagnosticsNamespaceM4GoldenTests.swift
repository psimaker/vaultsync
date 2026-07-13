import CryptoKit
import Foundation
import Testing

@Suite("Diagnostics namespace cross-language foundation (M4)")
struct DiagnosticsNamespaceM4GoldenTests {
    @Test("Decision 023 ownership records are byte-exact, signed, and digest-linked")
    func ownershipRecords() throws {
        let fixture = try M4NamespaceFixtureLoader.load()
        let contract = try M1ContractFixtureLoader.load()
        let names = [
            "01_enablement", "02_root_manifest", "03_helper_epoch",
            "04_initial_authorization", "05_authorization_epoch",
        ]
        #expect(fixture.keys.sorted() == names)

        var records: [String: (Data, [M1CBORField])] = [:]
        for (expectedType, name) in names.enumerated() {
            let encoded = try Data(m1Hex: #require(fixture[name]))
            let decoded = try M1DeterministicCBOR.decode(encoded)
            #expect(try M1DeterministicCBOR.encode(decoded) == encoded)
            let fields = try m4NamespaceFields(decoded)
            #expect(try m4NamespaceUnsigned(fields, label: 4) == UInt64(expectedType + 1))
            records[name] = (encoded, fields)
        }

        let enablement = try #require(records["01_enablement"])
        let rootManifest = try #require(records["02_root_manifest"])
        let helperEpochRecord = try #require(records["03_helper_epoch"])
        let initialAuthorization = try #require(records["04_initial_authorization"])
        let authorizationEpoch = try #require(records["05_authorization_epoch"])

        try m4VerifySignature(
            fields: enablement.1,
            keyLabel: 10, signatureLabel: 253,
            omitted: [253], domain: try #require(contract.domains["namespace.enablement_request"])
        )
        try m4VerifySignature(
            fields: rootManifest.1,
            keyLabel: 13, signatureLabel: 255,
            omitted: [255], domain: try #require(contract.domains["namespace.root_manifest"])
        )
        let helperEpoch = helperEpochRecord.1
        try m4VerifySignature(
            fields: helperEpoch, keyLabel: 16, signatureLabel: 254,
            omitted: [254, 255], domain: try #require(contract.domains["namespace.helper_epoch_prior"])
        )
        try m4VerifySignature(
            fields: helperEpoch, keyLabel: 13, signatureLabel: 255,
            omitted: [255], domain: try #require(contract.domains["namespace.helper_epoch_current"])
        )
        for (name, appDomain, helperDomain) in [
            ("04_initial_authorization", "namespace.authorization_initial_app", "namespace.authorization_initial_helper"),
            ("05_authorization_epoch", "namespace.authorization_epoch_app", "namespace.authorization_epoch_helper"),
        ] {
            let fields = try #require(records[name]?.1)
            try m4VerifySignature(
                fields: fields, keyLabel: 10, signatureLabel: 253,
                omitted: [253, 254, 255], domain: try #require(contract.domains[appDomain])
            )
            try m4VerifySignature(
                fields: fields, keyLabel: 13, signatureLabel: 255,
                omitted: [255], domain: try #require(contract.domains[helperDomain])
            )
        }

        let rootDigest = try m4RecordDigest(rootManifest.0)
        let helperEpochDigest = try m4RecordDigest(helperEpochRecord.0)
        let initialAuthorizationDigest = try m4RecordDigest(initialAuthorization.0)
        #expect(try m4NamespaceBytes(helperEpoch, label: 21, count: 32) == rootDigest)
        #expect(try m4NamespaceBytes(helperEpoch, label: 22, count: 32) == rootDigest)
        #expect(try m4NamespaceBytes(initialAuthorization.1, label: 23, count: 32) == helperEpochDigest)
        #expect(try m4NamespaceBytes(authorizationEpoch.1, label: 24, count: 32) == initialAuthorizationDigest)

        for name in ["04_initial_authorization", "05_authorization_epoch"] {
            let fields = try #require(records[name]?.1)
            var bindingInput = Data("eu.vaultsync.namespace/installation/v1\0".utf8)
            bindingInput.append(try m4NamespaceBytes(fields, label: 9, count: 32))
            bindingInput.append(try m4NamespaceBytes(fields, label: 5, count: 32))
            bindingInput.append(try m4NamespaceBytes(fields, label: 6, count: 32))
            #expect(Data(SHA256.hash(data: bindingInput)) == (try m4NamespaceBytes(fields, label: 8, count: 32)))
        }
    }

    @Test("Consent copy discloses visibility and retention in every supported language but stays unreferenced")
    func consentCopy() throws {
        let sourceKey = "Enable VaultSync Diagnostics for homeserver “%@” and vault “%@”? After separate confirmation by the local operator, this creates the visible app-owned folder “VaultSync Diagnostics”. It contains only opaque random protocol data, public-key identifiers, signatures, hashes, and expiry metadata—never note content or user-derived filenames. The folder, temporary files, and deletion tombstones synchronize and can be visible in Obsidian, Files, and on every configured peer. Backups, Syncthing versioning, remote history, and tombstones may retain artifacts after live cleanup. Disabling stops new operations but does not automatically delete the root, credentials, peer copies, backups, versions, or tombstones."
        let testsDirectory = URL(fileURLWithPath: "\(#filePath)").deletingLastPathComponent()
        let productDirectory = testsDirectory.deletingLastPathComponent().appendingPathComponent("VaultSync", isDirectory: true)
        let requiredTerms: [String: [String]] = [
            "en": ["Obsidian", "Files", "peer", "Backups", "versioning", "tombstones"],
            "de": ["Obsidian", "Dateien", "Peer", "Backups", "Versionierung", "Tombstones"],
            "es": ["Obsidian", "Archivos", "par", "copias de seguridad", "versionado", "registros de eliminación"],
            "zh-Hans": ["Obsidian", "文件", "对等设备", "备份", "版本", "删除记录"],
        ]

        for (locale, terms) in requiredTerms {
            let url = productDirectory
                .appendingPathComponent("\(locale).lproj", isDirectory: true)
                .appendingPathComponent("Localizable.strings")
            let data = try Data(contentsOf: url)
            let propertyList = try PropertyListSerialization.propertyList(from: data, format: nil)
            let strings = try #require(propertyList as? [String: String])
            let copy = try #require(strings[sourceKey])
            #expect(copy.components(separatedBy: "%@").count - 1 == 2)
            #expect(copy.contains("VaultSync Diagnostics"))
            for term in terms {
                #expect(copy.localizedCaseInsensitiveContains(term))
            }
        }

        let productSwiftFiles = try FileManager.default.subpathsOfDirectory(atPath: productDirectory.path)
            .filter { $0.hasSuffix(".swift") }
        for relativePath in productSwiftFiles {
            let body = try String(contentsOf: productDirectory.appendingPathComponent(relativePath), encoding: .utf8)
            #expect(!body.contains(sourceKey))
        }
    }
}

private final class M4NamespaceFixtureBundleToken {}

private enum M4NamespaceFixtureLoader {
    static func load(filePath: StaticString = #filePath) throws -> [String: String] {
        let bundle = Bundle(for: M4NamespaceFixtureBundleToken.self)
        let bundled = bundle.url(forResource: "diagnostics-namespace-m4", withExtension: "json")
        let sourceFallback = URL(fileURLWithPath: "\(filePath)")
            .deletingLastPathComponent()
            .appendingPathComponent("Fixtures", isDirectory: true)
            .appendingPathComponent("diagnostics-namespace-m4.json")
        return try JSONDecoder().decode([String: String].self, from: Data(contentsOf: bundled ?? sourceFallback))
    }
}

private func m4NamespaceFields(_ value: M1CBORValue) throws -> [M1CBORField] {
    guard case .map(let fields) = value else {
        throw M1ContractTestError.invalid("namespace record is not a map")
    }
    return fields
}

private func m4NamespaceUnsigned(_ fields: [M1CBORField], label: UInt64) throws -> UInt64 {
    guard case .unsigned(let value) = fields.first(where: { $0.label == label })?.value else {
        throw M1ContractTestError.invalid("namespace uint field is missing")
    }
    return value
}

private func m4NamespaceBytes(_ fields: [M1CBORField], label: UInt64, count: Int) throws -> Data {
    guard case .bytes(let value) = fields.first(where: { $0.label == label })?.value,
          value.count == count else {
        throw M1ContractTestError.invalid("namespace byte-string field is missing")
    }
    return value
}

private func m4VerifySignature(
    fields: [M1CBORField],
    keyLabel: UInt64,
    signatureLabel: UInt64,
    omitted: Set<UInt64>,
    domain: String
) throws {
    let signature = try m4NamespaceBytes(fields, label: signatureLabel, count: 64)
    let publicKeyBytes = try m4NamespaceBytes(fields, label: keyLabel, count: 32)
    let body = try M1DeterministicCBOR.encode(.map(fields.filter { !omitted.contains($0.label) }))
    var input = Data(domain.utf8)
    input.append(body)
    let key = try Curve25519.Signing.PublicKey(rawRepresentation: publicKeyBytes)
    #expect(key.isValidSignature(signature, for: input))

    var mutated = input
    mutated[mutated.startIndex] ^= 0x01
    #expect(!key.isValidSignature(signature, for: mutated))
}

private func m4RecordDigest(_ record: Data) throws -> Data {
    _ = try M1DeterministicCBOR.decode(record)
    var input = Data("eu.vaultsync.namespace/v1/record-digest\0".utf8)
    input.append(record)
    return Data(SHA256.hash(data: input))
}
