// Pre-guild URLs (bookmarks, old links) land on their new home.
import { error, redirect } from '@sveltejs/kit';
import { DEFAULT_GUILD } from '$lib/guild';

const g = `/g/${DEFAULT_GUILD}`;

const RULES: [RegExp, (m: RegExpMatchArray) => string][] = [
	[/^runs$/, () => `${g}/runs`],
	[/^runs\/([^/]+)$/, (m) => `${g}/runs/${m[1]}`],
	[/^pings\/([^/]+)$/, (m) => `${g}/inbox/${m[1]}`],
	[/^tickets(\/.*)?$/, (m) => `${g}/tickets${m[1] ?? ''}`],
	[/^projects$/, () => `${g}/w`],
	[/^projects\/new$/, () => `${g}/w/new`],
	[/^projects\/([^/]+)$/, (m) => `${g}/w/${m[1]}`],
	[/^flows$/, () => `${g}/flows`],
	[/^flows\/([^/]+)\/([^/]+)$/, (m) => `${g}/w/${m[1]}/flows/${m[2]}`],
	[/^integrations$/, () => `${g}/system/integrations`],
	[/^channels$/, () => `${g}/system/channels`],
	[/^settings$/, () => `${g}/system/limits`]
];

export const load = ({ params, url }) => {
	for (const [re, dest] of RULES) {
		const m = params.legacy.match(re);
		if (m) redirect(308, dest(m) + url.search + url.hash);
	}
	error(404, 'Not found');
};
