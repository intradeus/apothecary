// How long a personal token lasts, as its row on the Account page says it.
// A token with an inactivity window is the CLI's (`apothecary login` mints
// them): it signs out after that long unused, each use sliding it. Any other
// token lasts until its fixed expiry, or until revoked.

import type { Token } from '$lib/domain';
import { fmtAbs } from '$lib/core/format';
import { t } from '$lib/i18n/index.svelte';

const DAY = 86_400;
const HOUR = 3_600;

export interface TokenLife {
	/** "signs out after 7 days unused", "expires …"; '' when it never does. */
	text: string;
	/** Minted by the CLI's browser sign-in. */
	cli: boolean;
}

export function tokenLife(tk: Token): TokenLife {
	const idle = tk.idleSeconds ?? 0;
	if (idle > 0) {
		const text =
			idle >= DAY
				? t('page.account.idleDays', { count: Math.round(idle / DAY) })
				: t('page.account.idleHours', { count: Math.max(1, Math.round(idle / HOUR)) });
		return { text, cli: true };
	}
	if (tk.expiresAt)
		return { text: t('page.account.expires', { when: fmtAbs(tk.expiresAt) }), cli: false };
	return { text: '', cli: false };
}
