// Everything under /g/:guild is scoped to that guild. Unknown slugs 404
// rather than silently showing the default guild's data under a wrong name.
import { error } from '@sveltejs/kit';
import { GUILDS } from '$lib/guild';

export const load = ({ params }) => {
	if (!GUILDS.some((g) => g.slug === params.guild)) error(404, `No guild “${params.guild}”`);
};
