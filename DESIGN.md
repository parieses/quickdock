# Design System: QuickDock v3

## 1. Visual Theme & Atmosphere

QuickDock is a developer efficiency tool for Windows — a hybrid launcher/workspace manager like Raycast meets VS Code. The design language is **precision dark minimalism**: every pixel serves a purpose, spacing is intentional, and visual noise is aggressively reduced.

The dark theme is not flat black but a rich layered gray-scale with subtle blue undertones in surfaces, creating depth through value contrast rather than shadows. The light theme uses warm off-white tones for readability.

**Key Characteristics:**
- Dark chrome aesthetic with layered surface hierarchy (inspired by Raycast, Cursor)
- Blue accent (#4a9eff) for interactive elements — restrained, never decorative
- System font stack with monospace for technical labels
- Shadow-as-border technique (inspired by Vercel) — `box-shadow` replaces CSS borders for smoother rendering
- 8px base spacing with 4px increments for consistency
- Reduced motion preferred — 150ms transitions, no unnecessary animation
- Focus visible ring on all interactive elements for accessibility

## 2. Color Palette & Roles

### Dark Theme (default)

| Token | Value | Role |
|-------|-------|------|
| `--color-bg-primary` | `#1a1a1a` | Main canvas, page background |
| `--color-bg-secondary` | `#1e1e1e` | Sidebar, secondary surfaces |
| `--color-bg-tertiary` | `#242424` | Card backgrounds, hover states |
| `--color-bg-hover` | `#282828` | Interactive hover |
| `--color-bg-active` | `#323232` | Active/selected state |
| `--color-surface` | `#262626` | Elevated surface (dropdowns, menus) |
| `--color-surface-elevated` | `#2e2e2e` | Modal/dialog surface |
| `--color-text-primary` | `#e4e4e4` | Primary body text |
| `--color-text-secondary` | `#b0b0b0` | Secondary labels, descriptions |
| `--color-text-muted` | `#888888` | Placeholder, disabled text |
| `--color-text-disabled` | `#555555` | Disabled controls |
| `--color-border` | `#2e2e2e` | Subtle borders, dividers |
| `--color-border-light` | `#3a3a3a` | Hover borders |
| `--color-border-focus` | `#4a9eff` | Focus ring |
| `--color-accent` | `#4a9eff` | Primary accent, links, active |
| `--color-accent-hover` | `#3a8eef` | Accent hover |
| `--color-accent-text` | `#ffffff` | Text on accent |
| `--color-danger` | `#e84c4c` | Destructive actions |
| `--color-success` | `#4caf50` | Success states |
| `--color-warning` | `#ff9800` | Warning states |
| `--color-accent-bg` | `rgba(74, 158, 255, 0.1)` | Subtle accent background |
| `--color-accent-border` | `rgba(74, 158, 255, 0.2)` | Accent border |

### Light Theme

| Token | Value | Role |
|-------|-------|------|
| `--color-bg-primary` | `#f7f7f5` | Main canvas (warm off-white) |
| `--color-bg-secondary` | `#ffffff` | Sidebar, secondary surfaces |
| `--color-bg-tertiary` | `#efefeb` | Card backgrounds, hover |
| `--color-bg-hover` | `#e8e8e4` | Interactive hover |
| `--color-bg-active` | `#e0e0dc` | Active/selected |
| `--color-surface` | `#ffffff` | Elevated surface |
| `--color-surface-elevated` | `#f5f5f2` | Modal/dialog surface |
| `--color-text-primary` | `#1a1a18` | Primary body text |
| `--color-text-secondary` | `#4a4a48` | Secondary labels |
| `--color-text-muted` | `#888886` | Placeholder |
| `--color-text-disabled` | `#b0b0ae` | Disabled |
| `--color-border` | `#e4e4e0` | Subtle borders |
| `--color-border-light` | `#d0d0cc` | Hover borders |
| `--color-border-focus` | `#4a9eff` | Focus ring |

## 3. Typography Rules

### Font Family
- **UI**: `system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', sans-serif`
- **Monospace**: `'SF Mono', 'Fira Code', 'Consolas', monospace`

### Hierarchy

| Role | Size | Weight | Line Height | Use |
|------|------|--------|-------------|-----|
| Title Large | 16px | 600 | 1.4 | Section titles, card headings |
| Title Small | 14px | 600 | 1.4 | List item names, button text |
| Body | 13px | 400 | 1.5 | Primary content text |
| Body Small | 12px | 400 | 1.5 | Secondary text, descriptions |
| Caption | 11px | 500 | 1.4 | Labels, metadata, badges |
| Micro | 10px | 600 | 1.3 | Badge numbers, tiny labels |
| Mono Body | 13px | 400 | 1.5 | Code in content |
| Mono Small | 12px | 400 | 1.5 | Shortcuts, file paths |

### Principles
- **Single-weight hierarchy**: 400 (body), 500 (interactive), 600 (headings/emphasis). No 700+ weight.
- **System-first**: No custom fonts needed — system font stack performs better for desktop apps.
- **Compact but readable**: Base size 13px for dense data display with generous line-height 1.5.

## 4. Component Stylings

### Sidebar (210px)
- Background: `--color-bg-secondary`
- Right border: shadow-border `rgba(0,0,0,0.06) 1px 0 0 0`
- Workspace selector: 44px height, bottom border
- Scene items: 36px height, left active border (2px accent)
- Settings button: bottom-aligned, border-top separator
- Hover: subtle background shift

### Scene Tags
- Horizontal scrollable row, padding 8px 12px
- Tags as pills: height 28px, padding 0 12px, border-radius 14px
- Active: accent background, white text
- Inactive: tertiary background, muted text, hover → secondary text

### Collection List (300px)
- Background: `--color-bg-primary`
- Right border: shadow-border
- Each collection item: 44px height, icon + name + item count
- Drag handle on hover for reorder
- Create button: floating or embedded

### Item List
- Grid layout or list with 40px item height
- Item: icon (24px) + name + tool badge + action buttons (hover visible)
- Inline editing on click (not modal ideally)
- Open button always visible or hover-visible

### Buttons
- **Primary**: height 32px, padding 0 16px, radius 6px, accent bg, white text
- **Secondary/Icon**: 28x28px, radius 6px, transparent bg, hover → tertiary bg
- **Text**: transparent bg, accent text, hover underline

### Inputs
- Height 32px, padding 8px 12px, radius 6px
- Border: shadow-border technique
- Focus: ring 2px `--color-border-focus` with 15% opacity bg

### Badges
- Radius 4px, padding 2px 6px, font 11px weight 500
- Background `--color-bg-tertiary`, text `--color-text-muted`

### Scrollbar
- Width 6px, track transparent, thumb `--color-scrollbar-thumb` (#444 dark, #ccc light)
- Hover: thumb darkens by 10%

## 5. Layout Principles

### Spacing Scale
- **Base unit**: 8px
- **Scale**: 2, 4, 8, 12, 16, 20, 24, 32, 40, 48
- **Component padding**: 8-12px standard
- **Section padding**: 16-24px
- **Gap between items**: 4-8px

### App Layout
```
┌─────────┬──────────────────────────────────┐
│         │     SceneTags (40px)              │
│ Sidebar ├──────────────────────────────────┤
│ (210px) │  ┌────────┬──────────────────┐   │
│         │  │Collections│     Items      │   │
│         │  │ (300px)   │   (flex-1)    │   │
│         │  └────────┴──────────────────┘   │
└─────────┴──────────────────────────────────┘
```

### Elevation Levels
| Level | Shadow | Use |
|-------|--------|-----|
| 0 | None | Background surfaces |
| 1 | `0 1px 0 rgba(0,0,0,0.06)` — shadow border | Sidebar, panels |
| 2 | `0 1px 2px rgba(0,0,0,0.08)` | Hover items, tags |
| 3 | `0 4px 12px rgba(0,0,0,0.15)` | Dropdowns, popovers |
| 4 | `0 8px 24px rgba(0,0,0,0.2)` | Modals, dialogs |

## 6. Depth & Elevation

### Surface Hierarchy
- Level 0: Page background (`--color-bg-primary`)
- Level 1: Sidebar, content panels (`--color-bg-secondary`)
- Level 2: Inputs, cards, sections (`--color-bg-tertiary`)
- Level 3: Hover/active states (`--color-bg-hover`, `--color-bg-active`)
- Level 4: Dropdowns, popovers (`--color-surface`)
- Level 5: Modals, tooltips (`--color-surface-elevated`)

### Border Technique
- Use `box-shadow` for borders instead of `border` property (prevents layout shift)
- Pattern: `0 1px 0 rgba(0,0,0,0.06)` for a single border on one side
- Pattern: `inset 0 0 0 1px rgba(0,0,0,0.06)` for internal borders

### Focus Ring
- All interactive elements must have visible focus styles
- Pattern: `outline: none; box-shadow: 0 0 0 2px var(--color-border-focus)`
- Only visible on keyboard focus (use `:focus-visible`)

## 7. Do's and Don'ts

### Do
- Use the shadow-border technique for dividers and borders
- Use the spacing scale consistently (8px base)
- Use `:focus-visible` for focus rings (not `:focus`)
- Use 150ms ease transitions for interactive state changes
- Keep the accent color (blue) functional, not decorative
- Use text-style icons (Lucide) at consistent 16px size
- Use opacity for hover visibility on action buttons

### Don't
- Don't use pure black (`#000`) for dark backgrounds — use `#1a1a1a` instead
- Don't use heavy borders — shadow-border is always lighter
- Don't animate for animation's sake — only state transitions
- Don't use more than 3 surface levels in a single view
- Don't use border-top/bottom for section separation when shadow-border works
- Don't mix border-radius styles (6px standard, 4px small, 8px large)

## 8. Interaction & Motion

### Transitions
- Color/background: `150ms ease`
- Shadow/elevation: `200ms ease`
- Transform (scale/translate): `100ms ease`
- Opacity (show/hide): `200ms ease`

### Hover States
- Interactive elements get `--color-bg-hover` background on hover
- Icon buttons get circular/rounded background
- List items show action buttons on row hover
- Cards get subtle elevation increase

### Focus States
- `:focus-visible` ring: 2px solid `--color-border-focus`
- Never hide focus outlines unless providing alternative
- Input focus: border ring + subtle glow

## 9. Responsive Behavior

### Window Sizes
- Main window: 1100x700 default, min 800x500
- Clipboard window: 480x420, frameless/always-on-top
- Command palette: 620x480, frameless/always-on-top

### Adaptive Layout
- When window < 900px: collapse collections sidebar into overlay
- When window < 700px: scene tags wrap to two rows
- Sidebar minimum: 48px icon-only mode at extreme widths
- Scene tags horizontal scroll on overflow

### Touch
- Desktop only (no touch targets needed)
- All interactions keyboard-first
