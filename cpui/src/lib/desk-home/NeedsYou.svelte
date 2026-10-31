<script lang="ts">
	// "Needs you" panel (DESIGN 7.2): every run waiting on a human, oldest
	// first. One row per run — its newest pending ping is the one to answer;
	// earlier ones queue behind it (the inbox's rule). The whole row opens the
	// ping; the workshop link and the call to action sit above it.
	import { t } from '$lib/i18n/index.svelte';
	import { to } from '$lib/core/urls';
	import { fleet } from '$lib/app/fleet.svelte';
	import { age, fmtAbs, shortId } from '$lib/core/format';
	import { pingCta, pingHeadline } from '$lib/core/ping';
	import type { Ping } from '$lib/domain';
	import PingPill from '$lib/ui/PingPill.svelte';
	import { I } from '$lib/design/icons';
	import { bump } from '$lib/ui/motion';
	import WorkshopName from '$lib/app/WorkshopName.svelte';

	let { pings }: { pings: Ping[] } = $props();

	const rows = $derived.by(() => {
		const byRun = new Map<string, Ping[]>();
		for (const p of pings) byRun.set(p.runId, [...(byRun.get(p.runId) ?? []), p]);
		return [...byRun.values()]
			.map((g) => {
				const sorted = [...g].sort((a, b) => b.raisedAt.localeCompare(a.raisedAt));
				return { ping: sorted[0], queued: sorted.length - 1 };
			})
			.sort((a, b) => a.ping.raisedAt.localeCompare(b.ping.raisedAt));
	});
</script>

<section class="ny glass pane">
	<header class="pane-head">
		<span class="count hot mark" use:bump={pings.length}>{pings.length}</span>
		<h2>{t('deskhome.needs.title')}</h2>
		<span class="sub">{t('deskhome.needs.oldest')}</span>
		<span class="spacer"></span>
		<a class="small" href={to.inbox()}>{t('deskhome.needs.inbox')} {I.forward}</a>
	</header>
	<div class="rows">
		{#each rows as { ping: p, queued } (p.id)}
			{@const run = fleet.runById.get(p.runId)}
			<div class="r ny-r">
				<span class="ty"><PingPill type={p.type} /></span>
				<span class="what">
					<a class="q truncate stretch" href={to.ping(p.id)}
						>{pingHeadline(p.question) || t('deskhome.needs.fallback')}</a
					>
					<span class="sub truncate">
						<span class="mono">{shortId(p.runId)}</span>
						{' · '}{run?.title ?? t('deskhome.needs.runFallback')}{#if p.stageId}{' · '}<span
								class="mono">{p.stageId}</span
							>{/if}{#if queued > 0}<span class="hue-attention"
								>{' · '}{t('deskhome.needs.queued', { count: queued })}</span
							>{/if}
					</span>
				</span>
				<span class="ws truncate"><WorkshopName projectId={run?.projectId} link /></span>
				<span class="mono muted num age" title={fmtAbs(p.raisedAt)}>{age(p.raisedAt)}</span>
				<a class="btn btn-sel btn-sm over-stretch cta" href={to.ping(p.id)}>{pingCta(p)}</a>
			</div>
		{/each}
	</div>
</section>

<style>
	.ny {
		container: needs / inline-size;
		border-radius: var(--r-group);
	}
	h2 {
		white-space: nowrap;
	}
	header .sub {
		font-size: var(--fs-label);
		color: var(--text-muted);
		white-space: nowrap;
	}
	header a {
		white-space: nowrap;
	}
	.ny-r {
		grid-template-columns: 118px minmax(0, 1fr) 210px 64px auto;
		gap: 14px;
		padding: 10px 16px;
	}
	.rows > .ny-r:first-child {
		border-top: 0;
	}
	.ty {
		justify-self: start;
	}
	.what {
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.q {
		font-weight: 500;
		color: var(--text);
	}
	.q:hover {
		text-decoration: none;
	}
	.what .sub {
		font-size: var(--fs-help);
		color: var(--text-muted);
	}
	.what .sub .mono {
		font-size: var(--fs-meta);
	}
	.ws {
		display: flex;
		min-width: 0;
	}
	.age {
		font-size: var(--fs-meta);
	}
	@container needs (max-width: 800px) {
		.ny-r {
			grid-template-columns: auto minmax(0, 1fr) auto;
			grid-template-areas: 'ty age cta' 'what what what';
			row-gap: 6px;
		}
		.ty {
			grid-area: ty;
		}
		.what {
			grid-area: what;
		}
		.age {
			grid-area: age;
			text-align: left;
		}
		.cta {
			grid-area: cta;
		}
		.ws {
			display: none;
		}
		header .sub {
			display: none;
		}
	}
</style>
