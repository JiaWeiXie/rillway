# Bundled font inventory

The Web UI loads fonts from the same Rillway daemon. No external font service,
network connection to Google, or host font installation is required. The full
upstream font files are included, without subsetting, conversion, or modification.
This preserves their coverage for user-entered names as well as interface labels.

| File | Source revision | Size | License |
| --- | --- | --- | --- |
| `NotoSansTC.ttf` | google/fonts `9710da1eacb3be272583c3224dcb70f9da6eadbb` | 11,941,968 bytes | SIL OFL 1.1 |
| `NotoColorEmoji.ttf` | googlefonts/noto-emoji `e20cbc2bbec1926686be9f9bee7d1d2cfa1fea0e` | 10,730,124 bytes | SIL OFL 1.1 |

- [Noto Sans TC original variable font](https://github.com/google/fonts/blob/9710da1eacb3be272583c3224dcb70f9da6eadbb/ofl/notosanstc/NotoSansTC%5Bwght%5D.ttf), weights 100–900.
- [Noto Color Emoji original font](https://github.com/googlefonts/noto-emoji/blob/e20cbc2bbec1926686be9f9bee7d1d2cfa1fea0e/2D/fonts/NotoColorEmoji.ttf), CBDT/CBLC color format.
- [Noto Sans TC license](../../internal/control/web/fonts/OFL-NotoSansTC.txt).
- [Noto Color Emoji license](../../internal/control/web/fonts/OFL-NotoColorEmoji.txt).

SHA-256:

```text
864727d210d54f2537bbe23b3a839436c3992af72de9322af5270897246bd44f  NotoSansTC.ttf
15671215ab769fdc7162a045d56fd7d7e477c51b04e6b3c761d914d8fdd6cc44  NotoColorEmoji.ttf
1c05c68c34f9708415aada51f17e1b0092d2cea709bf4a94cd38114f9e73d7d9  OFL-NotoSansTC.txt
6a73f9541c2de74158c0e7cf6b0a58ef774f5a780bf191f2d7ec9cc53efe2bf2  OFL-NotoColorEmoji.txt
```

Native color emoji fonts are preferred where available, with the bundled Noto
font as a fallback. Emoji appearance and support for the newest sequences depend
on the browser and font version. In particular, Safari on macOS uses Apple Color
Emoji; the bundled CBDT/CBLC font is intended for browsers that support that format.
The terminal controls TUI font rendering separately; see [language support](../i18n.md).

Both fonts and their licenses are embedded in the application. Release builds also
copy their licenses to `dist/_licenses/fonts/`. Font files add about 21.6 MiB to an
uncompressed binary and are cached by the browser after first use.
