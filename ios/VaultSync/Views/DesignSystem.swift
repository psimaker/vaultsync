import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

// MARK: - Shared UI component kit
//
// The reusable building blocks of the design. Each replaces a pattern that was
// hand-rebuilt across many views, so "what a status / card / row looks like"
// is edited once here instead of at dozens of call sites. All consume the
// tokens in Theme.swift (colors, tones, spacing, radius, the SyncStatus
// registry). The #187 kit — page, hero, card group, row, chip, buttons —
// implements the approved "calm, status-first" canvas.
//
// App-only (not in the widget target), so these may use `L10n`.

// MARK: Status primitives

/// The savings tag on the subscription plan picker ("Save N%"). It carries
/// the primary button's measured pair — `vaultOnAccent` on
/// `vaultAccentProminent`, at least 4.8:1 in light, dark and Increase
/// Contrast (#199) — because it sits right above the Subscribe button.
/// Every other short state word uses `StatusChip`.
struct StatusTag: View {
    let text: String

    var body: some View {
        Text(text)
            .font(.caption2.weight(.bold))
            .foregroundStyle(Color.vaultOnAccent)
            // Wraps instead of cutting off: at the accessibility text sizes
            // a cut tag lost its number ("Ahorra…", #352). The corner radius
            // is clamped to half the height, so one line still reads as a
            // capsule; two lines keep their corners clear of the text.
            .fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, VaultSpacing.s)
            .padding(.vertical, VaultSpacing.xxs)
            .background(Color.vaultAccentProminent, in: RoundedRectangle(cornerRadius: VaultRadius.control, style: .continuous))
    }
}

/// An attention/error card: status glyph + title + plain-language message + a real
/// primary action button (≥44pt) and optional secondary link. Replaces the
/// icon+title+message+remediation block that was hand-rebuilt at least three times,
/// and turns prose "go to Settings" remediations into a tappable action.
///
/// #187: urgent statuses render on their own wash (the canvas's "needs your
/// decision" card) instead of a 4pt accent strip; the status word stays in
/// the VoiceOver value because the wash alone carries no meaning.
struct ActionCard: View {
    let status: SyncStatus
    let title: String
    var message: String?
    var actionTitle: String?
    var action: (() -> Void)?
    var secondary: (() -> AnyView)?

    var body: some View {
        VStack(alignment: .leading, spacing: VaultSpacing.m) {
            HStack(alignment: .top, spacing: VaultSpacing.m) {
                Image(systemName: status.symbolName)
                    .font(.title3)
                    .foregroundStyle(status.tint)
                    .vaultLeadingSlot()
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                    Text(title)
                        .font(.body.weight(.semibold))
                        .foregroundStyle(Color.vaultLabel)
                        .fixedSize(horizontal: false, vertical: true)
                    if let message {
                        Text(message)
                            .font(.subheadline)
                            .foregroundStyle(Color.vaultSecondaryLabel)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .accessibilityElement(children: .combine)
            .accessibilityValue(status.label)
            if let actionTitle, let action {
                Button(actionTitle, action: action)
                    .buttonStyle(.vault(.primary, compact: true))
            }
            if let secondary {
                secondary()
            }
        }
        .padding(VaultSpacing.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .vaultCard(tone: status.isUrgent ? status.tone : nil)
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
            // Revert the affordance so the field doesn't latch on "copied" forever.
            Task {
                try? await Task.sleep(for: .seconds(1.5))
                withAnimation(.snappy) { copied = false }
            }
        } label: {
            HStack(alignment: .top, spacing: VaultSpacing.s) {
                Text(text)
                    .font(.vaultMono(.footnote))
                    .lineLimit(3)
                    .truncationMode(.middle)
                    .frame(maxWidth: .infinity, alignment: .leading)
                Image(systemName: copied ? "checkmark" : "doc.on.doc")
                    .foregroundStyle(copied ? Color.statusSuccess : Color.vaultAccent)
                    .accessibilityHidden(true)
            }
            .padding(VaultSpacing.m)
            .background(
                Color.vaultNeutralFill,
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

// MARK: - Card surface

private struct VaultCardModifier: ViewModifier {
    var tone: VaultTone?
    var radius: CGFloat

    func body(content: Content) -> some View {
        let shape = RoundedRectangle(cornerRadius: radius, style: .continuous)
        content
            .background {
                // The wash is composited over the card surface, not over the
                // page: the text-on-wash tokens were measured against
                // wash-over-white (light) / wash-over-#171D20 (dark).
                ZStack {
                    Color.vaultSurface
                    if let tone {
                        tone.fill
                    }
                }
                .clipShape(shape)
            }
            .overlay {
                // A tinted card draws no hairline — the wash is its edge.
                if tone == nil {
                    shape.strokeBorder(Color.vaultHairline, lineWidth: 1)
                }
            }
    }
}

extension View {
    /// Standard card surface (#187): white / slate-dark card, 16pt continuous
    /// corners, a slate hairline — or, with a tone, that tone's wash and no
    /// hairline (the canvas's "needs your decision" and "waiting for you"
    /// cards).
    func vaultCard(tone: VaultTone? = nil, radius: CGFloat = VaultRadius.card) -> some View {
        modifier(VaultCardModifier(tone: tone, radius: radius))
    }
}

// MARK: - Page scaffold (#187)

/// A scrolling page on the vault background: 20pt side margins, 12pt between
/// cards, and a readable-width column on iPad so cards never stretch across
/// a 13-inch screen. Navigation chrome (large title, toolbar) stays the
/// system's, which keeps Liquid Glass on iOS 26 and classic bars on iOS 18.
struct VaultPage<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: VaultSpacing.m) {
                content
            }
            .frame(maxWidth: VaultMetrics.readableWidth, alignment: .leading)
            .padding(.horizontal, VaultSpacing.gutter)
            .padding(.top, VaultSpacing.s)
            .padding(.bottom, VaultSpacing.xl)
            .frame(maxWidth: .infinity)
        }
        .background(Color.vaultBackground.ignoresSafeArea())
    }
}

/// Uppercase caption above a card group ("NEEDS YOUR DECISION", "VAULTS").
struct VaultSectionHeader: View {
    let title: String

    init(_ title: String) {
        self.title = title
    }

    var body: some View {
        Text(title)
            .font(.vaultSectionHeader)
            .textCase(.uppercase)
            .tracking(0.4)
            .foregroundStyle(Color.vaultSecondaryLabel)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, VaultSpacing.xs)
            .padding(.top, VaultSpacing.s)
            .accessibilityAddTraits(.isHeader)
    }
}

/// A card that stacks its children as rows with hairline dividers between
/// them. Conditional and `ForEach` children flatten into individual rows, and
/// a group whose children all resolve to nothing draws no empty card.
struct VaultCardGroup<Content: View>: View {
    var tone: VaultTone?
    @ViewBuilder var content: Content

    init(tone: VaultTone? = nil, @ViewBuilder content: () -> Content) {
        self.tone = tone
        self.content = content()
    }

    var body: some View {
        Group(subviews: content) { rows in
            if !rows.isEmpty {
                VStack(spacing: 0) {
                    ForEach(rows) { row in
                        row
                        if row.id != rows.last?.id {
                            Rectangle()
                                .fill(Color.vaultHairline)
                                .frame(height: 1)
                                .padding(.horizontal, VaultSpacing.l)
                                .accessibilityHidden(true)
                        }
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .clipShape(RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous))
                .vaultCard(tone: tone)
            }
        }
    }
}

/// One row in a `VaultCardGroup` (60pt minimum): leading glyph, title,
/// optional subtitle, trailing accessory. The text pair reads as one
/// VoiceOver element; a trailing button stays independently focusable.
struct VaultRow<Trailing: View>: View {
    let title: String
    var subtitle: String?
    var systemImage: String?
    var iconTint: Color
    var monospacedSubtitle: Bool
    /// A spinner replaces the glyph (e.g. a device reconnecting).
    var busy: Bool
    @ViewBuilder var trailing: Trailing

    init(
        _ title: String,
        subtitle: String? = nil,
        systemImage: String? = nil,
        iconTint: Color = .vaultAccent,
        monospacedSubtitle: Bool = false,
        busy: Bool = false,
        @ViewBuilder trailing: () -> Trailing = { EmptyView() }
    ) {
        self.title = title
        self.subtitle = subtitle
        self.systemImage = systemImage
        self.iconTint = iconTint
        self.monospacedSubtitle = monospacedSubtitle
        self.busy = busy
        self.trailing = trailing()
    }

    var body: some View {
        HStack(spacing: VaultSpacing.m) {
            if busy {
                ProgressView()
                    .vaultLeadingSlot()
                    .accessibilityHidden(true)
            } else if let systemImage {
                Image(systemName: systemImage)
                    .font(.title3)
                    .foregroundStyle(iconTint)
                    .vaultLeadingSlot()
                    .accessibilityHidden(true)
            }
            VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
                Text(title)
                    .font(.body)
                    .foregroundStyle(Color.vaultLabel)
                    .fixedSize(horizontal: false, vertical: true)
                if let subtitle {
                    Text(subtitle)
                        .font(monospacedSubtitle ? .vaultMono(.footnote) : .footnote)
                        .foregroundStyle(Color.vaultSecondaryLabel)
                        .lineLimit(monospacedSubtitle ? 3 : nil)
                        .truncationMode(.middle)
                        .fixedSize(horizontal: false, vertical: !monospacedSubtitle)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityElement(children: .combine)
            trailing
        }
        .padding(.horizontal, VaultSpacing.l)
        .padding(.vertical, 10)
        .frame(minHeight: VaultMetrics.rowMinHeight)
        .contentShape(Rectangle())
    }
}

/// The leading glyph column of rows and cards. Its 28pt width at the
/// default text size scales with the `.title3` glyph it holds: a fixed
/// width let the glyph outgrow it at accessibility sizes and run into the
/// text beside it (#209). The row's 12pt spacing then always stays clear.
private struct VaultLeadingSlot: ViewModifier {
    @ScaledMetric(relativeTo: .title3) private var width: CGFloat = 28

    func body(content: Content) -> some View {
        content.frame(width: width)
    }
}

extension View {
    /// Puts a `.title3` glyph (or a spinner in its place) in the leading
    /// column of a row or card.
    func vaultLeadingSlot() -> some View {
        modifier(VaultLeadingSlot())
    }
}

/// Disclosure chevron for navigating rows.
struct VaultChevron: View {
    var body: some View {
        Image(systemName: "chevron.right")
            .font(.footnote.weight(.semibold))
            .foregroundStyle(Color.vaultSecondaryLabel)
            .accessibilityHidden(true)
    }
}

/// 10pt status dot at the end of a row. Decorative: the row's subtitle
/// carries the status word, so the dot is hidden from VoiceOver.
struct StatusDot: View {
    var status: SyncStatus?

    var body: some View {
        Circle()
            .fill(status?.tint ?? Color.statusInactive)
            .frame(width: VaultMetrics.statusDotSize, height: VaultMetrics.statusDotSize)
            .accessibilityHidden(true)
    }
}

/// Capsule chip for short facts and states ("2 vaults", "Cloud Relay
/// active"). Tone picks wash + text together, so contrast holds.
struct StatusChip: View {
    let text: String
    var tone: VaultTone = .neutral
    var systemImage: String?

    var body: some View {
        HStack(spacing: VaultSpacing.xs) {
            if let systemImage {
                Image(systemName: systemImage)
                    .accessibilityHidden(true)
            }
            Text(text)
                .fixedSize(horizontal: false, vertical: true)
        }
        .font(.vaultChip)
        .foregroundStyle(tone.text)
        .padding(.horizontal, 10)
        .padding(.vertical, VaultSpacing.xs)
        .background(tone.fill, in: Capsule())
    }
}

/// The one status hero per screen (#187): a round status well, the title in
/// hero type, a subtitle, and a wrapping row of chips. Reads as ONE VoiceOver
/// element: title as label, subtitle and chips as the value.
struct StatusHeroCard<Chips: View>: View {
    let status: SyncStatus
    let title: String
    var subtitle: String?
    /// Glyph override for heroes that are not a sync status (Cloud Relay).
    var systemImage: String?
    /// Spinner in the well instead of the glyph (reconnecting).
    var busy: Bool
    /// Trailing chevron when the whole card is a button.
    var showsDisclosure: Bool
    @ViewBuilder var chips: Chips

    init(
        status: SyncStatus,
        title: String,
        subtitle: String? = nil,
        systemImage: String? = nil,
        busy: Bool = false,
        showsDisclosure: Bool = false,
        @ViewBuilder chips: () -> Chips = { EmptyView() }
    ) {
        self.status = status
        self.title = title
        self.subtitle = subtitle
        self.systemImage = systemImage
        self.busy = busy
        self.showsDisclosure = showsDisclosure
        self.chips = chips()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: VaultSpacing.m) {
                VaultHeroHeader(
                    tone: status.tone,
                    systemImage: systemImage ?? status.wellSymbolName,
                    title: title,
                    subtitle: subtitle,
                    pulses: status == .syncing,
                    busy: busy
                )
                if showsDisclosure {
                    VaultChevron()
                }
            }

            VaultFlowLayout(spacing: VaultSpacing.s) {
                chips
            }
        }
        .padding(VaultSpacing.gutter)
        .frame(maxWidth: .infinity, alignment: .leading)
        .vaultCard(radius: VaultRadius.hero)
        .accessibilityElement(children: .combine)
    }
}

/// The top of a hero card: the round well and the title pair. Its own
/// component so a hero that also holds a button (the Cloud Relay
/// celebration) can keep that button focusable outside the combined text.
/// Takes a tone and an explicit pulse rather than a `SyncStatus`, so a hero
/// that is not a sync state (Cloud Relay, M2's pairing steps) neither
/// borrows a status's spoken label nor its transfer animation.
struct VaultHeroHeader: View {
    let tone: VaultTone
    let systemImage: String
    let title: String
    var subtitle: String?
    /// Pulse the glyph — an active transfer, nothing else.
    var pulses = false
    /// Spinner in the well instead of the glyph (reconnecting).
    var busy = false

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        HStack(spacing: VaultSpacing.m) {
            ZStack {
                Circle()
                    .fill(tone.fill)
                if busy {
                    ProgressView()
                        .tint(tone.tint)
                } else {
                    Image(systemName: systemImage)
                        .font(.title3.weight(.semibold))
                        .foregroundStyle(tone.tint)
                        .contentTransition(.symbolEffect(.replace))
                        .symbolEffect(.pulse, isActive: pulses && !reduceMotion)
                }
            }
            .frame(width: VaultMetrics.heroIconSize, height: VaultMetrics.heroIconSize)
            .accessibilityHidden(true)

            VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
                Text(title)
                    .font(.vaultHeroTitle)
                    .foregroundStyle(Color.vaultLabel)
                    .fixedSize(horizontal: false, vertical: true)
                if let subtitle {
                    Text(subtitle)
                        .font(.subheadline)
                        .foregroundStyle(Color.vaultSecondaryLabel)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

/// Left-to-right wrapping layout for chips: as many per line as fit, the
/// rest on the next line. A chip wider than the line wraps its own text.
struct VaultFlowLayout: Layout {
    var spacing: CGFloat = VaultSpacing.s

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rows = arrange(width: proposal.width ?? .infinity, subviews: subviews)
        let height = rows.last.map { $0.y + $0.height } ?? 0
        let width = rows.map(\.width).max() ?? 0
        return CGSize(width: proposal.width ?? width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let rows = arrange(width: bounds.width, subviews: subviews)
        for row in rows {
            for item in row.items {
                subviews[item.index].place(
                    at: CGPoint(x: bounds.minX + item.x, y: bounds.minY + row.y),
                    proposal: ProposedViewSize(item.size)
                )
            }
        }
    }

    private struct Item {
        let index: Int
        let x: CGFloat
        let size: CGSize
    }

    private struct Row {
        var y: CGFloat
        var height: CGFloat = 0
        var width: CGFloat = 0
        var items: [Item] = []
    }

    private func arrange(width maxWidth: CGFloat, subviews: Subviews) -> [Row] {
        var rows: [Row] = []
        var current = Row(y: 0)
        for index in subviews.indices {
            let size = subviews[index].sizeThatFits(ProposedViewSize(width: maxWidth, height: nil))
            let x = current.items.isEmpty ? 0 : current.width + spacing
            if !current.items.isEmpty, x + size.width > maxWidth {
                rows.append(current)
                current = Row(y: current.y + current.height + spacing)
                current.items.append(Item(index: index, x: 0, size: size))
                current.width = size.width
                current.height = size.height
            } else {
                current.items.append(Item(index: index, x: x, size: size))
                current.width = x + size.width
                current.height = max(current.height, size.height)
            }
        }
        if !current.items.isEmpty {
            rows.append(current)
        }
        return rows
    }
}

/// Short note on a tinted wash (e.g. "the optional Cloud Relay lives on the
/// Relay tab"): glyph + footnote, one VoiceOver element.
struct VaultNotice: View {
    let systemImage: String
    let text: String
    var tone: VaultTone = .info

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: systemImage)
                .foregroundStyle(tone.tint)
                .accessibilityHidden(true)
            Text(text)
                .font(.footnote)
                .foregroundStyle(Color.vaultLabel)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .vaultCard(tone: tone, radius: VaultRadius.button)
        .accessibilityElement(children: .combine)
    }
}

/// Designed empty / gone state inside a card: accent well, title, optional
/// message, optional actions.
struct VaultEmptyState<Actions: View>: View {
    let systemImage: String
    let title: String
    var message: String?
    @ViewBuilder var actions: Actions

    init(
        systemImage: String,
        title: String,
        message: String? = nil,
        @ViewBuilder actions: () -> Actions = { EmptyView() }
    ) {
        self.systemImage = systemImage
        self.title = title
        self.message = message
        self.actions = actions()
    }

    var body: some View {
        VStack(spacing: VaultSpacing.m) {
            Image(systemName: systemImage)
                .font(.title2)
                .foregroundStyle(Color.vaultAccent)
                .frame(width: 56, height: 56)
                .background(Color.vaultAccentFill, in: Circle())
                .accessibilityHidden(true)
            VStack(spacing: VaultSpacing.xs) {
                Text(title)
                    .font(.headline)
                    .foregroundStyle(Color.vaultLabel)
                if let message {
                    Text(message)
                        .font(.subheadline)
                        .foregroundStyle(Color.vaultSecondaryLabel)
                }
            }
            .multilineTextAlignment(.center)
            .fixedSize(horizontal: false, vertical: true)
            .accessibilityElement(children: .combine)
            actions
        }
        .padding(VaultSpacing.gutter)
        .frame(maxWidth: .infinity)
        .vaultCard()
    }
}

/// Two or three buttons side by side, stacked once Dynamic Type reaches the
/// accessibility sizes so no label gets squeezed into a sliver.
struct VaultButtonRow<Content: View>: View {
    @ViewBuilder var content: Content

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        let layout = dynamicTypeSize.isAccessibilitySize
            ? AnyLayout(VStackLayout(spacing: VaultSpacing.s))
            : AnyLayout(HStackLayout(alignment: .top, spacing: VaultSpacing.s))
        layout {
            content
        }
    }
}

// MARK: - Buttons (#187)

/// Full-width rounded buttons from the canvas: `.primary` (deep teal),
/// `.tinted` (teal wash), `.neutral` (slate wash), `.destructive` (error
/// wash). Heights are minimums, so labels grow with Dynamic Type.
struct VaultButtonStyle: ButtonStyle {
    enum Kind {
        case primary
        case tinted
        case neutral
        case destructive
    }

    var kind: Kind
    var compact = false

    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.vaultButton)
            .multilineTextAlignment(.center)
            .foregroundStyle(foreground)
            .padding(.horizontal, 18)
            .padding(.vertical, VaultSpacing.s)
            .frame(
                maxWidth: .infinity,
                minHeight: compact ? VaultMetrics.compactButtonHeight : VaultMetrics.buttonHeight
            )
            .background(background, in: RoundedRectangle(cornerRadius: VaultRadius.button, style: .continuous))
            .contentShape(RoundedRectangle(cornerRadius: VaultRadius.button, style: .continuous))
            .opacity(isEnabled ? (configuration.isPressed ? 0.7 : 1) : 0.4)
            .animation(.easeOut(duration: 0.12), value: configuration.isPressed)
    }

    private var foreground: Color {
        switch kind {
        case .primary: return .vaultOnAccent
        case .tinted: return .vaultAccentText
        case .neutral: return .vaultLabel
        case .destructive: return .statusErrorText
        }
    }

    private var background: Color {
        switch kind {
        case .primary: return .vaultAccentProminent
        case .tinted: return .vaultAccentFill
        case .neutral: return .vaultNeutralFill
        case .destructive: return .statusErrorFill
        }
    }
}

extension ButtonStyle where Self == VaultButtonStyle {
    static func vault(_ kind: VaultButtonStyle.Kind, compact: Bool = false) -> VaultButtonStyle {
        VaultButtonStyle(kind: kind, compact: compact)
    }
}

/// Inline text action ("Review", "How is this private?"): accent text with
/// the 44pt touch minimum.
struct VaultLinkButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.subheadline.weight(.semibold))
            .foregroundStyle(Color.vaultAccentText)
            .frame(minHeight: VaultMetrics.compactButtonHeight)
            .contentShape(Rectangle())
            .opacity(configuration.isPressed ? 0.6 : 1)
    }
}

extension ButtonStyle where Self == VaultLinkButtonStyle {
    static var vaultLink: VaultLinkButtonStyle { VaultLinkButtonStyle() }
}

/// Tappable card row (navigation or action): a slate press highlight across
/// the full row, clipped by the card's corners.
struct VaultRowButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .background(configuration.isPressed ? Color.vaultNeutralFill : Color.clear)
            .contentShape(Rectangle())
    }
}

extension ButtonStyle where Self == VaultRowButtonStyle {
    static var vaultRow: VaultRowButtonStyle { VaultRowButtonStyle() }
}

// MARK: - System list screens (#187)

extension View {
    /// Puts a system `List`/`Form` on the vault page background so the
    /// secondary screens that keep system rows (filters, diagnostics, setup
    /// guides) share the new canvas instead of the neutral grouped gray.
    func vaultListBackground() -> some View {
        scrollContentBackground(.hidden)
            .background(Color.vaultBackground.ignoresSafeArea())
    }

    /// Gives the rows of a system `List`/`Form` the card surface (#201).
    /// The system row is `#1C1C1E` in dark, next to `#171D20` cards on
    /// every other screen. `listRowBackground` is a row trait: it acts on
    /// the rows and sections it is applied to, never on the `List` itself.
    func vaultListRows() -> some View {
        listRowBackground(Color.vaultSurface)
    }
}

/// A system `List` on the vault page with card-surface rows (#201): for the
/// secondary screens that keep system rows (filters, diagnostics, setup
/// guides, the activity log).
struct VaultList<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View {
        List {
            Group {
                content
            }
            .vaultListRows()
        }
        .vaultListBackground()
    }
}
