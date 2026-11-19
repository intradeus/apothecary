// A fill made on one page for another: a run's change to its flow, taken to
// the flow editor. Held in this tab's session storage for the editor to
// take once, as it opens; lost with the tab, never sent anywhere.

const key = (workshop: string, flow: string) => `apothecary.flowFill.${workshop}.${flow}`;

/** Hold a flow's proposed YAML for its editor. false: no storage here. */
export function stashFlowFill(workshop: string, flow: string, yaml: string): boolean {
	try {
		sessionStorage.setItem(key(workshop, flow), yaml);
		return true;
	} catch {
		return false;
	}
}

/** The YAML held for this flow, taken (it is offered once), or null. */
export function takeFlowFill(workshop: string, flow: string): string | null {
	try {
		const k = key(workshop, flow);
		const v = sessionStorage.getItem(k);
		sessionStorage.removeItem(k);
		return v;
	} catch {
		return null;
	}
}
