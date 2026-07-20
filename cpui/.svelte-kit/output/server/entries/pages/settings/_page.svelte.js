import "clsx";
function _page($$renderer, $$props) {
  $$renderer.component(($$renderer2) => {
    $$renderer2.push(`<h1>Settings</h1> <p class="muted small" style="margin-top:-8px">Policy knobs, applied on the next admission decision — no restart needed.
  Connections live under <a href="/integrations">Integrations</a>; notification mirrors under <a href="/channels">Channels</a>.</p> `);
    {
      $$renderer2.push("<!--[-1-->");
      $$renderer2.push(`<div class="empty">Loading…</div>`);
    }
    $$renderer2.push(`<!--]-->`);
  });
}
export {
  _page as default
};
