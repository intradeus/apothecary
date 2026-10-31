Apothecary — logo kit (mark 7c, flask A)
Colours: Ember #ff6a2b · Ink #170a04 · Paper #f3ece4
Fonts: Bricolage Grotesque 700, tracking -40 (wordmark) · Hanken Grotesk 600, tracking +240 (tagline)

mark/svg        vector masters (bubbles are true cut-outs)
mark/png        tiles + glyphs, exact pixel sizes (file name = px)
app-icons       favicon 16/32, apple-touch 180, android 192/512, safari pinned tab
lockups         transparent PNG, tight crop, @1x/@2x/@4x. on-dark = paper text, on-light = ink text
                @1x sizes: horizontal on-dark: 301×49 @1x; horizontal on-light: 301×49 @1x; horizontal-tagline on-dark: 289×64 @1x; horizontal-tagline on-light: 289×64 @1x; stacked on-dark: 210×115 @1x; stacked on-light: 210×115 @1x; wordmark on-dark: 288×50 @1x; wordmark on-light: 288×50 @1x; wordmark-tagline on-dark: 259×65 @1x; wordmark-tagline on-light: 259×65 @1x
social          1200x630 card
SVG vs PNG — which to use
  SVG (masters, use these first): mark/svg, lockups/svg (text outlined, no font needed),
      app-icons/favicon.svg, app-icons/safari-pinned-tab.svg, web/ (sprite + currentColor glyph for the app header)
  PNG (only where the platform needs raster): apple-touch / android icons, favicon-16/32 fallback,
      social card (has grain texture), lockups/*.png for email, Slack, docs and other places that won't take SVG.
lockups/svg variants: on-dark (paper text), on-light (ink text), mono-ink (single colour for print and embossing).
Web favicon:  <link rel="icon" href="favicon.svg" type="image/svg+xml"> + <link rel="icon" href="favicon-32x32.png" sizes="32x32">
App header:   <svg width="36" height="36"><use href="apothecary-sprite.svg#tile"/></svg>
