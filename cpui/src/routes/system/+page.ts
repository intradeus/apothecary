import { redirect } from '@sveltejs/kit';
import { to } from '$lib/urls';

export const load = () => {
	redirect(307, to.system('integrations'));
};
