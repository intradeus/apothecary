// How long a personal token lasts, as its row on the Account page says it,
// and whether it is over: the engine's own two rules (auth.AuthenticateToken),
// read off the clock the page gives.
//
// A token with an inactivity window is the CLI's (`apothecary login` mints
// them): it signs out after that long unused, each use sliding it. Any other
// token lasts until its fixed expiry, or until revoked. One past either is
// refused by the engine and kept in its list until someone revokes it.

import type { Token } from '$lib/domain';
import { ago, fmtAbs, fromNow } from '$lib/core/format';
import { t } from '$lib/i18n/index.svelte';

const DAY = 86_400;
const HOUR = 3_600;

export interface TokenLife {
	/** "expires in 3d", "expired 2h ago", "signs out after 7 days unused",
	 *  "signed out 1d ago", "never expires". */
	text: string;
	/** The moment `text` counts to or from, in full, for its tooltip; '' when
	 *  it names none. */
	at: string;
	/** Minted by the CLI's browser sign-in. */
	cli: boolean;
	/** Past its expiry or its inactivity window: the engine refuses it. */
	expired: boolean;
}

export function tokenLife(tk: Token, now = Date.now()): TokenLife {
	const idle = tk.idleSeconds ?? 0;
	const cli = idle > 0;
	if (cli) {
		const out = new Date(tk.lastUsedAt ?? tk.createdAt).getTime() + idle * 1000;
		if (now >= out)
			return {
				text: t('page.account.signedOut', { ago: ago(out, now) }),
				at: fmtAbs(out),
				cli,
				expired: true
			};
	}
	const end = tk.expiresAt ? new Date(tk.expiresAt).getTime() : null;
	if (end !== null && now >= end)
		return {
			text: t('page.account.expired', { ago: ago(end, now) }),
			at: fmtAbs(end),
			cli,
			expired: true
		};
	if (cli) {
		const text =
			idle >= DAY
				? t('page.account.idleDays', { count: Math.round(idle / DAY) })
				: t('page.account.idleHours', { count: Math.max(1, Math.round(idle / HOUR)) });
		return { text, at: '', cli, expired: false };
	}
	if (end !== null)
		return {
			text: t('page.account.expires', { when: fromNow(end, now) }),
			at: fmtAbs(end),
			cli,
			expired: false
		};
	return { text: t('page.account.neverExpires'), at: '', cli, expired: false };
}
