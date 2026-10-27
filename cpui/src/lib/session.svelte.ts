// Who is signed in (TODO 25). The shell asks the engine once at boot: does
// the instance need its first account, and is this browser signed in? Until
// it knows, nothing renders; then the setup page, the sign-in page or the
// app. A 401 from any later call (signed out elsewhere, session expired,
// account disabled) drops the shell back to sign-in without a reload.

import { api } from '$lib/api';
import { onUnauthorized } from '$lib/api/client';
import type { Me, User } from '$lib/domain';

class Session {
	/** The boot check has answered. */
	ready = $state(false);
	/** No account exists yet: the setup page is the only page. */
	setupRequired = $state(false);
	/** Authentication methods the instance offers ('password', …). */
	methods = $state<string[]>([]);
	me = $state<Me | null>(null);
	/** The boot check failed (the engine is down): shown instead of a page. */
	error = $state('');

	readonly user = $derived<User | null>(this.me?.user ?? null);
	readonly admin = $derived(!!this.me?.user.admin);

	constructor() {
		onUnauthorized(() => this.expire());
	}

	/** The boot check. Safe to call again (a retry after an outage). */
	async load() {
		this.error = '';
		try {
			const status = await api.auth.status();
			this.setupRequired = status.setupRequired;
			this.methods = status.methods;
			if (!status.setupRequired) {
				try {
					this.me = await api.auth.me();
				} catch {
					this.me = null; // not signed in: the sign-in page
				}
			}
			this.ready = true;
		} catch (e) {
			this.error = e instanceof Error ? e.message : String(e);
		}
	}

	async setup(d: { handle: string; email: string; name: string; password: string }) {
		this.me = await api.auth.setup(d);
		this.setupRequired = false;
	}

	async login(login: string, password: string) {
		this.me = await api.auth.login(login, password);
	}

	async logout() {
		try {
			await api.auth.logout();
		} finally {
			this.me = null;
		}
	}

	/** The engine said 401: whatever we thought, we are signed out. */
	expire() {
		if (this.me) this.me = null;
	}

	/** After a profile edit. */
	async refresh() {
		this.me = await api.auth.me();
	}
}

export const session = new Session();
