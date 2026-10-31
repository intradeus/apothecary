<script lang="ts">
	// Additional repositories (multi-repo tasks): cloned beside the primary,
	// each published as its own merge request when a run changes it.
	import type { ExtraRepo } from '$lib/domain';
	import { I } from '$lib/design/icons';
	import { t, th } from '$lib/i18n/index.svelte';
	import FormSub from './FormSub.svelte';

	let { rows = $bindable(), onadd }: { rows: ExtraRepo[]; onadd: () => void } = $props();

	function remove(i: number) {
		rows = rows.filter((_, j) => j !== i);
	}
</script>

<FormSub>
	<div class="row">
		<h3>{t('form.project.extraHead')}</h3>
		<span class="count">{rows.length}</span>
		<span class="spacer"></span>
		<button type="button" class="btn-sm" onclick={() => onadd()}
			>{I.add} {t('form.project.addRepo')}</button
		>
	</div>
	<div class="help">{@html th('form.project.extraHelp')}</div>
	{#if rows.length}
		<table class="xt form-dense">
			<thead>
				<tr
					><th>{t('form.project.col.name')}</th><th>{t('form.project.col.url')}</th><th
						>{t('form.project.col.locator')}</th
					><th>{t('form.project.col.branch')}</th><th></th></tr
				>
			</thead>
			<tbody>
				{#each rows as r, i (i)}
					<tr>
						<td
							><input
								id={`extra-${i}-name`}
								class="mono"
								bind:value={r.name}
								placeholder="api"
								aria-label={t('form.project.checkoutName')}
								required
							/></td
						>
						<td
							><input
								class="mono"
								bind:value={r.url}
								placeholder="https://gitlab.com/group/api.git"
								aria-label={t('form.project.repoUrlAria')}
								required
							/></td
						>
						<td
							><input
								class="mono"
								bind:value={r.locator}
								placeholder={t('form.project.readOnly')}
								aria-label={t('form.project.locatorAria')}
							/></td
						>
						<td
							><input
								class="mono"
								bind:value={r.defaultBranch}
								placeholder="main"
								aria-label={t('form.project.branchAria')}
							/></td
						>
						<td
							><button
								type="button"
								class="btn-icon"
								onclick={() => remove(i)}
								aria-label={t('form.project.removeRepo', {
									name: r.name || t('form.project.repository')
								})}>{I.close}</button
							></td
						>
					</tr>
				{/each}
			</tbody>
		</table>
		<div class="help">{t('form.project.primaryHelp')}</div>
	{/if}
</FormSub>

<style>
	/* Extra repositories: a table of inputs; tight cells, no row rules. */
	.xt th,
	.xt td {
		padding: 3px 3px;
		border-top: 0;
	}
	.xt th {
		position: static;
	}
	.xt td:last-child {
		width: 28px;
	}
	.xt th:first-child {
		width: 130px;
	}
	.xt th:nth-child(3) {
		width: 120px;
	}
	.xt th:nth-child(4) {
		width: 110px;
	}
	@media (max-width: 760px) {
		.xt thead {
			display: none;
		}
		.xt tr {
			display: grid;
			grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) 28px;
		}
	}
</style>
