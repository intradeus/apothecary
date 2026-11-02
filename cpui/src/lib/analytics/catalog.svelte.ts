// The analytics catalog: every grain and the fields a query may name on it.
// The engine's whitelist; it never changes while the app runs, so it is
// fetched once and shared by every panel, editor and filter bar.

import { errorMessage } from '$lib/core/errors';
import { api } from '$lib/api';
import type { Catalog, CatalogField, CatalogGrain } from '$lib/domain';

class CatalogStore {
	data = $state<Catalog | null>(null);
	error = $state('');
	#pending: Promise<void> | null = null;

	/** Fetch once. Concurrent callers share the request; after a failure the
	 *  next call tries again. */
	load(): Promise<void> {
		if (this.data) return Promise.resolve();
		this.#pending ??= (async () => {
			this.error = '';
			try {
				this.data = await api.analytics.catalog();
			} catch (e) {
				this.error = errorMessage(e);
			} finally {
				this.#pending = null;
			}
		})();
		return this.#pending;
	}

	grain(id: string): CatalogGrain | undefined {
		return this.data?.grains.find((g) => g.id === id);
	}

	/** A dimension, measure or derived measure of the grain. */
	field(grain: string, id: string): CatalogField | undefined {
		const g = this.grain(grain);
		return g && [...g.dimensions, ...g.measures, ...g.derived].find((f) => f.id === id);
	}

	/** Back to unloaded (tests). */
	reset() {
		this.data = null;
		this.error = '';
		this.#pending = null;
	}
}

export const catalog = new CatalogStore();
