// The cross-guild inbox. With one guild it is that guild's inbox.
import { redirect } from '@sveltejs/kit';
import { GUILDS } from '$lib/guild';

export const load = () => {
	redirect(307, `/g/${GUILDS[0].slug}/inbox`);
};
