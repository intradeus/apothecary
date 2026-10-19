// "/" is the All-guilds desk for someone in several guilds; with one guild
// (the engine has no organisations yet) it is that guild's desk.
import { redirect } from '@sveltejs/kit';
import { GUILDS } from '$lib/guild';

export const load = () => {
	if (GUILDS.length === 1) redirect(307, `/g/${GUILDS[0].slug}`);
};
