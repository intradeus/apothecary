import { a as attr_class } from "../../chunks/index.js";
import { p as page } from "../../chunks/index2.js";
function _layout($$renderer, $$props) {
  $$renderer.component(($$renderer2) => {
    let { children } = $$props;
    const isActive = (path) => path === "/" ? page.url.pathname === "/" || page.url.pathname.startsWith("/runs") : page.url.pathname.startsWith(path);
    $$renderer2.push(`<nav class="top"><span class="brand">⚗ Apothecary</span> <a href="/"${attr_class("", void 0, { "active": isActive("/") })}>Runs</a> <a href="/inbox"${attr_class("", void 0, { "active": isActive("/inbox") })}>Inbox`);
    {
      $$renderer2.push("<!--[-1-->");
    }
    $$renderer2.push(`<!--]--></a> <a href="/projects"${attr_class("", void 0, { "active": isActive("/projects") })}>Projects</a> <a href="/flows"${attr_class("", void 0, { "active": isActive("/flows") })}>Flows</a> <a href="/integrations"${attr_class("", void 0, { "active": isActive("/integrations") })}>Integrations</a> <a href="/settings"${attr_class("", void 0, { "active": isActive("/settings") })}>Settings</a> <a href="/channels"${attr_class("", void 0, { "active": isActive("/channels") })}>Channels</a></nav> <div class="container">`);
    children($$renderer2);
    $$renderer2.push(`<!----></div>`);
  });
}
export {
  _layout as default
};
