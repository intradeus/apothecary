<script lang="ts">
	// What a merge request's line says under its title: who opened it, and
	// what is worth knowing at a glance — a draft, a fork, the one it is
	// stacked on (linked), and how many runs it has been through here.
	// "Processed" is what the data can say for sure: a run has it, started
	// on it or as the one it opened. Who opened it is the host's author, not
	// a guess from the integration's account.
	import { t } from '$lib/i18n/index.svelte';
	import type { MergeRequestRow } from '$lib/domain';

	let { row }: { row: MergeRequestRow } = $props();
</script>

<span class="small faint meta"
	>{row.mr.author}{#if row.runs > 0}
		· <span class="pill sm tint-brand" title={t('workshop.mrs.processedTitle')}
			>{t('workshop.mrs.processed', { count: row.runs })}</span
		>{/if}{#if row.mr.draft}
		· <span class="pill sm tint-muted">{t('workshop.mrs.draft')}</span>{/if}{#if row.mr.fork}
		· <span class="pill sm tint-muted">{t('workshop.mrs.fork')}</span>{/if}{#if row.stackedOn}
		· <a
			class="pill sm tint-warning"
			href={row.stackedOn.url}
			target="_blank"
			rel="noreferrer"
			title={t('workshop.mrs.stackedTitle', {
				iid: String(row.stackedOn.iid),
				title: row.stackedOn.title,
				branch: row.stackedOn.branch
			})}>{t('workshop.mrs.stacked', { iid: String(row.stackedOn.iid) })}</a
		>{/if}</span
>
