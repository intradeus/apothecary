import { e as escape_html, b as ensure_array_like, d as attr } from "../../../chunks/index.js";
import { a as ago } from "../../../chunks/api.js";
import "@sveltejs/kit/internal";
import "../../../chunks/exports.js";
import "../../../chunks/utils2.js";
import "@sveltejs/kit/internal/server";
import "../../../chunks/root.js";
import "../../../chunks/state.svelte.js";
function _page($$renderer, $$props) {
  $$renderer.component(($$renderer2) => {
    let flows = [];
    $$renderer2.push(`<div class="row spread" style="margin-bottom:16px"><h1 style="margin:0">Flows</h1> <button class="primary">${escape_html("+ New flow")}</button></div> `);
    {
      $$renderer2.push("<!--[-1-->");
    }
    $$renderer2.push(`<!--]--> `);
    const each_array = ensure_array_like(flows);
    if (each_array.length !== 0) {
      $$renderer2.push("<!--[-->");
      for (let $$index = 0, $$length = each_array.length; $$index < $$length; $$index++) {
        let f = each_array[$$index];
        $$renderer2.push(`<a class="card row spread cardlink"${attr("href", `/flows/${encodeURIComponent(f.name)}`)}><div><strong class="mono">${escape_html(f.name)}</strong> <div class="small muted">hash ${escape_html(f.hash.slice(0, 12))} · updated ${escape_html(ago(f.updated_at))}</div></div> <span class="muted">→</span></a>`);
      }
    } else {
      $$renderer2.push("<!--[!-->");
      $$renderer2.push(`<div class="empty">${escape_html("Loading…")}</div>`);
    }
    $$renderer2.push(`<!--]-->`);
  });
}
export {
  _page as default
};
