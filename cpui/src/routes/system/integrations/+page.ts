import { redirect } from '@sveltejs/kit';
import { INTEGRATION_PORTS, to } from '$lib/core/urls';

// Integrations has no page of its own: each kind has one.
export const load = () => {
	redirect(307, to.integrations(INTEGRATION_PORTS[0]));
};
