// The lore vocabulary (DESIGN §1): a small set of renamed nouns shown through
// the flavor switch. Ticket, stage, ping, workspace, MR, egress and every
// state name stay technical in both flavors — never add those here.

import { prefs } from '$lib/prefs.svelte';

const WORDS = {
	guild: ['Organisation', 'Guild'],
	guilds: ['Organisations', 'Guilds'],
	workshop: ['Project', 'Workshop'],
	workshops: ['Projects', 'Workshops'],
	run: ['Run', 'Batch'],
	flow: ['Flow', 'The formula'],
	plan: ['Plan', 'The recipe'],
	needsYou: ['Needs you', 'Awaiting your hand'],
	composer: ['Message builder…', 'Whisper to the builder…'],
	emptyDesk: ['No windows open', 'An empty bench'],
	egressBlocked: ['', 'toxic hosts, kept off the shelf'],
	newGuild: ['+ New organisation', '+ Found a guild'],
	tagline: ['agent workflows', 'software, bottled'],
	nothingRunning: ['Nothing running', 'All still on the shelf'],
	brewing: ['', 'brewing'],
	spoiled: ['', 'batch spoiled']
} as const;

export type LoreWord = keyof typeof WORDS;

/** The word in the current flavor. Reactive when read inside a component. */
export function lore(w: LoreWord): string {
	return WORDS[w][prefs.flavor === 'lore' ? 1 : 0];
}

export const isLore = () => prefs.flavor === 'lore';

/** The noun lowercased, for mid-sentence use ("this workshop" / "this project"). */
export const loreLc = (w: LoreWord): string => lore(w).toLowerCase();

/** A phrase written in both flavors. Use it for any copy that leans on the
 *  lore (brewing, the bench, the shelf…): plain mode must never show it. */
export const fl = (loreText: string, plainText: string): string =>
	prefs.flavor === 'lore' ? loreText : plainText;
