<script lang="ts" module>
	/** One port of the table. `exposed` picks the status's tint; the titles
	 *  say more on hover (the status's reason, the port's in-workspace
	 *  address). */
	export interface PortRow {
		port: number;
		name?: string;
		url?: string;
		status: string;
		exposed: boolean;
		statusTitle?: string;
		portTitle?: string;
	}
</script>

<script lang="ts">
	// A table of workspace ports, the Ports and Services views' alike: a row
	// each — its name, the port, its status, the `apothecary forward` line
	// that brings it onto the person's own machine (any port, any protocol),
	// and "open" in the browser: a link once the port has one, else a button
	// that exposes it first when the caller can. The link is a web proxy, so
	// it only serves web apps; the caller says so under the table, once.
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import CopyButton from '$lib/ui/CopyButton.svelte';

	let {
		rows,
		nameHeader,
		forward,
		links,
		onopen,
		busy = null
	}: {
		rows: PortRow[];
		/** The first column's heading; none, no such column. */
		nameHeader?: string;
		/** A port's forward command; none, no such column (nothing to reach). */
		forward?: (port: number) => string;
		/** Whether the engine hands out browser links: the Browser column. */
		links: boolean;
		/** Exposes a port that has no link yet; none, such a row offers none. */
		onopen?: (port: number) => void;
		/** The port being exposed: every open button waits for it. */
		busy?: number | null;
	} = $props();
</script>

<table class="port-table">
	<thead>
		<tr>
			{#if nameHeader}<th>{nameHeader}</th>{/if}
			<th>{t('runview.ports.col_port')}</th>
			<th>{t('runview.ports.col_status')}</th>
			{#if forward}<th>{t('runview.ports.col_forward')}</th>{/if}
			{#if links}<th>{t('runview.ports.col_browser')}</th>{/if}
		</tr>
	</thead>
	<tbody>
		{#each rows as r (r.port)}
			<tr>
				{#if nameHeader}<td class:none={!r.name}>{r.name ?? '—'}</td>{/if}
				<td class="mono pn" title={r.portTitle}>:{r.port}</td>
				<td>
					<span class="pill sm" class:tint-success={r.exposed} title={r.statusTitle}
						>{r.status}</span
					>
				</td>
				{#if forward}
					<td class="act">
						<CopyButton command={forward(r.port)} label={t('runview.ports.forward_btn')} />
					</td>
				{/if}
				{#if links}
					<td class="act">
						{#if r.url}
							<a
								class="btn btn-sm"
								href={r.url}
								target="_blank"
								rel="noreferrer"
								title={r.url}
								aria-label={t('runview.ports.open_aria', { port: String(r.port) })}
								>{t('runview.ports.open')} {I.external}</a
							>
						{:else if onopen}
							<button
								class="btn-sm"
								disabled={busy !== null}
								aria-busy={busy === r.port}
								aria-label={t('runview.ports.open_aria', { port: String(r.port) })}
								onclick={() => onopen(r.port)}>{t('runview.ports.open')} {I.external}</button
							>
						{/if}
					</td>
				{/if}
			</tr>
		{/each}
	</tbody>
</table>

<style>
	.port-table th,
	.port-table td {
		padding: 6px 8px;
	}
	.act {
		white-space: nowrap;
	}
	.none {
		color: var(--text-muted);
	}
	.pn {
		color: var(--accent-2-text);
	}
</style>
