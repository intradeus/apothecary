import { e as escape_html, b as ensure_array_like, c as attr_style, s as stringify, d as attr } from "../../chunks/index.js";
import { s as stateColors, a as ago } from "../../chunks/api.js";
import "@sveltejs/kit/internal";
import "../../chunks/exports.js";
import "../../chunks/utils2.js";
import "@sveltejs/kit/internal/server";
import "../../chunks/root.js";
import "../../chunks/state.svelte.js";
function _page($$renderer, $$props) {
  $$renderer.component(($$renderer2) => {
    let runs = [];
    $$renderer2.push(`<div class="row spread" style="margin-bottom:16px"><h1 style="margin:0">Runs</h1> <button class="primary">${escape_html("+ New ticket")}</button></div> `);
    {
      $$renderer2.push("<!--[-1-->");
    }
    $$renderer2.push(`<!--]--> `);
    if (runs.length === 0) {
      $$renderer2.push("<!--[0-->");
      $$renderer2.push(`<div class="empty">${escape_html("Loading…")}</div>`);
    } else {
      $$renderer2.push("<!--[-1-->");
      $$renderer2.push(`<div class="card" style="padding:4px 0"><table><thead><tr><th>Ticket</th><th>State</th><th>Stage</th><th>Flow</th><th>MR</th><th>Updated</th></tr></thead><tbody><!--[-->`);
      const each_array = ensure_array_like(runs);
      for (let $$index = 0, $$length = each_array.length; $$index < $$length; $$index++) {
        let r = each_array[$$index];
        $$renderer2.push(`<tr class="click"><td>${escape_html(r.ticket_title)}<div class="mono muted">${escape_html(r.run_id)}</div></td><td><span class="chip"><span class="dot"${attr_style(`background:${stringify(stateColors[r.state])}`)}></span>${escape_html(r.state)}</span></td><td class="mono">${escape_html(r.current_stage ?? "")}</td><td class="mono">${escape_html(r.flow_name)}</td><td>`);
        if (r.mr_url) {
          $$renderer2.push("<!--[0-->");
          $$renderer2.push(`<a${attr("href", r.mr_url)} target="_blank" rel="noreferrer">MR ↗</a>`);
        } else {
          $$renderer2.push("<!--[-1-->");
        }
        $$renderer2.push(`<!--]--></td><td class="muted small">${escape_html(ago(r.updated_at))}</td></tr>`);
      }
      $$renderer2.push(`<!--]--></tbody></table></div>`);
    }
    $$renderer2.push(`<!--]-->`);
  });
}
export {
  _page as default
};
