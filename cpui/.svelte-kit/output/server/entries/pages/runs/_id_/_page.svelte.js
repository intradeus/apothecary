import { e as escape_html } from "../../../../chunks/index.js";
import "clsx";
import { p as page } from "../../../../chunks/index2.js";
function _page($$renderer, $$props) {
  $$renderer.component(($$renderer2) => {
    page.params.id ?? "";
    {
      $$renderer2.push("<!--[-1-->");
      $$renderer2.push(`<div class="empty">${escape_html("Loading…")}</div>`);
    }
    $$renderer2.push(`<!--]-->`);
  });
}
export {
  _page as default
};
