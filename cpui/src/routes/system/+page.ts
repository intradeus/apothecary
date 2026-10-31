import { redirect } from '@sveltejs/kit';
import { to } from '$lib/core/urls';

export const load = () => {
	redirect(307, to.system('integrations'));
};
