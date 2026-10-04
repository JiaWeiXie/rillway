# Rillway brand assets

The Rillway symbol combines an **R** with a branching river. It represents choosing an outbound for each connection. The shared line is **Choose your route.**

The product interface supports English (`en`, default) and Traditional Chinese (`zh-Hant`): Web UI, TUI, CLI help, and known Rillway-owned status and error messages. User-entered names and unknown diagnostics returned by other software retain their original language. See [language support](../i18n.md).

## Assets

| Asset | Size | Use |
| --- | --- | --- |
| [Transparent symbol](../../internal/control/web/brand/rillway-mark.png) | 1254 × 1254, RGBA PNG | Login, sidebar, favicon, project avatar |
| [Flow illustration](../../internal/control/web/brand/rillway-flow.png) | 1536 × 1024, RGB PNG | Login artwork and presentation backgrounds |
| [Project cover](rillway-cover.png) | 1774 × 887, RGB PNG | Repository header and sharing |

These are raster originals, not editable SVG artwork. The symbol has real transparency. Preserve its aspect ratio and leave clear space around it; use a light surface behind the dark strokes in a dark interface. The flow illustration is decorative and carries no network status information.

The login and sidebar assets are embedded in the Go binary and served locally. The cover remains a documentation asset to keep it out of the application payload. Browser favicons reuse the transparent symbol. No external images or font service are required.

## Visual direction

| Role | Color |
| --- | --- |
| Petrol ink | `#164B55` |
| Jade route | `#199E92` |
| Pale mint | `#B6DFD8` |
| Cool canvas | `#EDF3F4` |
| Surface | `#FFFFFF` |

Use the generated symbol as the identity; keep the operational UI quiet and readable. The Web UI includes Noto Sans TC for Chinese and Noto Color Emoji as an emoji fallback, alongside native system fonts. Sources and licenses are recorded in [the font inventory](fonts.md). Use sentence case for English labels and reserve monospaced text for code/configuration.

## Provenance

Created with the built-in ImageGen tool on 2026-10-04. The symbol was generated first; the illustration and cover use it as a visual reference. All selected PNGs are saved in this repository. The exact prompts and reference roles are recorded in [prompts.md](prompts.md).
