import { redirect } from '@sveltejs/kit';
import { to } from '$lib/guild';

export const load = ({ params }) => {
	redirect(307, to.system('integrations', params.guild));
};
