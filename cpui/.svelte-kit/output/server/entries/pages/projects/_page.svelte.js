import { b as ensure_array_like, d as attr, e as escape_html } from "../../../chunks/index.js";
import "@sveltejs/kit/internal";
import "../../../chunks/exports.js";
import "../../../chunks/utils2.js";
import "@sveltejs/kit/internal/server";
import "../../../chunks/root.js";
import "../../../chunks/state.svelte.js";
function _page($$renderer, $$props) {
  $$renderer.component(($$renderer2) => {
    let projects = [];
    let integrations = [];
    const intName = (id) => integrations.find((i) => i.id === id)?.name ?? id.slice(-6);
    $$renderer2.push(`<div class="row spread" style="margin-bottom:16px"><h1 style="margin:0">Projects</h1> <button class="primary">+ New project</button></div> `);
    {
      $$renderer2.push("<!--[-1-->");
    }
    $$renderer2.push(`<!--]--> `);
    const each_array = ensure_array_like(projects);
    if (each_array.length !== 0) {
      $$renderer2.push("<!--[-->");
      for (let $$index = 0, $$length = each_array.length; $$index < $$length; $$index++) {
        let p = each_array[$$index];
        $$renderer2.push(`<a class="card row spread cardlink"${attr("href", `/projects/${p.id}`)}><div><strong>${escape_html(p.name)}</strong> <div class="mono small muted">${escape_html(p.repo.url)}</div> <div class="small muted">forge: ${escape_html(intName(p.forge_id))} · workspaces: ${escape_html(intName(p.provisioner_id))} · harness:
        ${escape_html(intName(p.harness_id))} · flow: ${escape_html(p.default_flow)}</div></div> <span class="muted">→</span></a>`);
      }
    } else {
      $$renderer2.push("<!--[!-->");
      $$renderer2.push(`<div class="empty">`);
      {
        $$renderer2.push("<!--[0-->");
        $$renderer2.push(`Loading…`);
      }
      $$renderer2.push(`<!--]--></div>`);
    }
    $$renderer2.push(`<!--]-->`);
  });
}
export {
  _page as default
};
