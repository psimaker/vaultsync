import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

// MARK: - Shared UI component kit
//
// The reusable building blocks of the redesign. Each replaces a pattern the
// audit found hand-rebuilt across many views, so "what a status / card / row
// looks like" is edited once here instead of at dozens of call sites. All consume
// the tokens in Theme.swift (colors, spacing, radius, the SyncStatus registry).
//
// App-only (not in the widget target), so these may use `L10n`.

// MARK: Status primitives

/// Icon + text label for a sync state, driven by the `SyncStatus` registry so
/// status is NEVER conveyed by color alone — the symbol and the text carry the
/// meaning for VoiceOver and color-blind users; the color is redundant emphasis.
struct StatusBadge: View {
    let status: SyncStatus
    /// Optional override for the registry's default label.
    var text: String?

    init(_ status: SyncStatus, text: String? = nil) {
        self.status = status
        self.text = text
    }

    var body: some View {
        HStack(spacing: VaultSpacing.xs) {
            Image(systemName: status.symbolName)
                .foregroundStyle(status.tint)
                .accessibilityHidden(true)
            Text(text ?? status.label)
                .font(.subheadline.weight(.medium))
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel(text ?? status.label)
    }
}

/// A list row: leading status glyph, primary title, optional secondary line, and
/// optional trailing content. Replaces the title+caption `VStack(spacing: 2)` that
/// was copy-pasted across five views.
struct StatusRow<Trailing: View>: View {
    let title: String
    var subtitle: String?
    var status: SyncStatus?
    var systemImage: String?
    @ViewBuilder var trailing: () -> Trailing

    init(
        _ title: String,
        subtitle: String? = nil,
        status: SyncStatus? = nil,
        systemImage: String? = nil,
        @ViewBuilder trailing: @escaping () -> Trailing = { EmptyView() }
    ) {
        self.title = title
        self.subtitle = subtitle
        self.status = status
        self.systemImage = systemImage
        self.trailing = trailing
    }

    private var glyph: String? { systemImage ?? status?.symbolName }

    var body: some View {
        HStack(spacing: VaultSpacing.m) {
            if let glyph {
                Image(systemName: glyph)
                    .font(.title3)
                    .foregroundStyle(status?.tint ?? Color.vaultAccent)
                    .frame(width: 28)
                    .accessibilityHidden(true)
            }
            VStack(alignment: .leading, spacing: VaultSpacing.xs / 2) {
                Text(title)
                    .font(.headline)
                if let subtitle {
                    Text(subtitle)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
            }
            Spacer(minLength: VaultSpacing.s)
            trailing()
        }
        .accessibilityElement(children: .combine)
    }
}

/// Title + value row (e.g. a key/value detail). The de-facto `DetailRow` the
/// audit found duplicated across views.
struct DetailRow: View {
    let title: String
    let value: String
    var monospacedValue: Bool = false

    var body: some View {
        HStack(spacing: VaultSpacing.m) {
            Text(title)
                .foregroundStyle(.secondary)
            Spacer(minLength: VaultSpacing.s)
            Text(value)
                .font(monospacedValue ? .system(.body, design: .monospaced) : .body)
                .multilineTextAlignment(.trailing)
        }
        .font(.subheadline)
        .accessibilityElement(children: .combine)
    }
}

/// An attention/error card: status glyph + title + plain-language message + a real
/// primary action button (≥44pt) and optional secondary link. Replaces the
/// icon+title+message+remediation block that was hand-rebuilt at least three times,
/// and turns prose "go to Settings" remediations into a tappable action.
struct ActionCard: View {
    let status: SyncStatus
    let title: String
    var message: String?
    var actionTitle: String?
    var action: (() -> Void)?
    var secondary: (() -> AnyView)?

    var body: some View {
        VStack(alignment: .leading, spacing: VaultSpacing.s) {
            StatusBadge(status, text: title)
                .font(.headline)
            if let message {
                Text(message)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let actionTitle, let action {
                Button(actionTitle, action: action)
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
            }
            if let secondary {
                secondary()
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(VaultSpacing.l)
        .vaultCard(tint: status.isUrgent ? status.tint : nil)
    }
}

/// A copyable monospaced field for genuine machine strings — Device IDs, `.stignore`
/// globs, `docker run` commands. Tap to copy with a haptic + visual confirmation.
/// This "monospace-as-identity, tap-to-copy" treatment is the Vault OS signature:
/// the domain reality is a first-class citizen, not a leak to apologize for.
struct MonoField: View {
    let text: String
    var accessibilityName: String?

    @State private var copied = false

    var body: some View {
        Button {
            #if canImport(UIKit)
            UIPasteboard.general.string = text
            UIImpactFeedbackGenerator(style: .light).impactOccurred()
            #endif
            withAnimation(.snappy) { copied = true }
        } label: {
            HStack(alignment: .top, spacing: VaultSpacing.s) {
                Text(text)
                    .font(.system(.footnote, design: .monospaced))
                    .lineLimit(3)
                    .truncationMode(.middle)
                    .frame(maxWidth: .infinity, alignment: .leading)
                Image(systemName: copied ? "checkmark" : "doc.on.doc")
                    .foregroundStyle(copied ? Color.statusSuccess : Color.vaultAccent)
                    .accessibilityHidden(true)
            }
            .padding(VaultSpacing.m)
            .background(
                Color(.secondarySystemBackground),
                in: RoundedRectangle(cornerRadius: VaultRadius.control, style: .continuous)
            )
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(accessibilityName ?? text)
        .accessibilityValue(copied ? L10n.tr("Copied") : "")
        .accessibilityHint(L10n.tr("Double tap to copy"))
        .accessibilityAddTraits(.isButton)
    }
}

// MARK: - Persistent sync-status header

/// The "answer at a glance" header: a floating material bar that states one
/// unambiguous truth about the vault — the canonical `SyncStatus` drives the
/// glyph + color, and the title/subtitle carry the contextual detail. Pinned
/// above the content via `.safeAreaInset(edge: .top)`. On iOS 26 the material
/// adopts the Liquid Glass look automatically.
struct SyncStatusHeader: View {
    let status: SyncStatus
    let title: String
    var subtitle: String?
    /// When true, show an indeterminate spinner instead of the status glyph
    /// (used while reconnecting to peers).
    var busy: Bool = false

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        HStack(spacing: VaultSpacing.m) {
            ZStack {
                if busy {
                    ProgressView()
                        .tint(status.tint)
                } else {
                    Image(systemName: status.symbolName)
                        .font(.title2)
                        .foregroundStyle(status.tint)
                        .contentTransition(.symbolEffect(.replace))
                        .symbolEffect(.pulse, isActive: status == .syncing && !reduceMotion)
                }
            }
            .frame(width: 30, height: 30)
            .accessibilityHidden(true)

            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.headline)
                if let subtitle {
                    Text(subtitle)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, VaultSpacing.l)
        .padding(.vertical, VaultSpacing.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.regularMaterial)
        .overlay(alignment: .bottom) {
            Divider()
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel(title)
        .accessibilityValue(subtitle ?? "")
    }
}

// MARK: - Card surface

private struct VaultCardModifier: ViewModifier {
    var tint: Color?

    func body(content: Content) -> some View {
        content
            .background(
                Color(.secondarySystemBackground),
                in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous)
            )
            .overlay(alignment: .leading) {
                if let tint {
                    Rectangle()
                        .fill(tint)
                        .frame(width: 4)
                        .clipShape(
                            RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous)
                        )
                        .accessibilityHidden(true)
                }
            }
    }
}

extension View {
    /// Standard card surface: continuous-radius `card` corner, secondary system
    /// background, and an optional leading status-accent bar. Replaces the three
    /// divergent card looks (custom fills / system grouped / hand-rolled
    /// RoundedRect + 0.5pt stroke) with one.
    func vaultCard(tint: Color? = nil) -> some View {
        modifier(VaultCardModifier(tint: tint))
    }
}
