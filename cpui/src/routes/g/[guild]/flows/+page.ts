// The cross-workshop flows overview folded into each workshop's Flows tab
// (DESIGN 7.7). Old links land on the workshops index.
import { redirect } from '@sveltejs/kit';
import { to } from '$lib/guild';

export const load = ({ params }) => {
	redirect(307, to.workshops(params.guild));
};
