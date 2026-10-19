<script lang="ts">
	// "Needs you" panel (DESIGN 7.2): every run waiting on a human, oldest
	// first. One row per run — its newest pending ping is the one to answer;
	// earlier ones queue behind it (the inbox's rule).
	import { to } from '$lib/guild';
	import { fleet } from '$lib/fleet.svelte';
	import { age, fmtAbs, shortId } from '$lib/format';
	import { lore } from '$lib/lore';
	import { pingCta, pingHeadline } from '$lib/ping';
	import type { Ping } from '$lib/domain';
	import PingPill from '$lib/ui/PingPill.svelte';
	import Vial from '$lib/ui/Vial.svelte';

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

<section class="ny c">
	<header>
		<span class="n">{pings.length}</span>
		<h2>{lore('needsYou')}</h2>
		<span class="sub">oldest first</span>
		<span class="spacer"></span>
		<a href={to.inbox()}>Open inbox →</a>
	</header>
	{#each rows as { ping: p, queued } (p.id)}
		{@const run = fleet.runById.get(p.runId)}
		{@const pid = run?.projectId}
		<div class="row-ny">
			<span class="ty"><PingPill type={p.type} /></span>
			<span class="what">
				<a class="q ell" href={to.ping(p.id)}>{pingHeadline(p.question) || 'A question for you'}</a>
				<span class="sub ell">
					<span class="mono">{shortId(p.runId)}</span>
					{' · '}{run?.title ?? 'run'}{#if p.stageId}{' · '}<span class="mono">{p.stageId}</span
						>{/if}{#if queued > 0}<span class="amb">{' · '}{queued} more queued</span>{/if}
				</span>
			</span>
			<span class="ws ell">
				{#if pid}<Vial projectId={pid} />{/if}
				<span class="ell">{(pid && fleet.projectById.get(pid)?.name) ?? '—'}</span>
			</span>
			<span class="age mono" title={fmtAbs(p.raisedAt)}>{age(p.raisedAt)}</span>
			<a class="btn btn-sel cta" href={to.ping(p.id)}>{pingCta(p)}</a>
		</div>
	{/each}
</section>

<style>
	.ny {
		border-radius: 18px;
		overflow: hidden;
		display: flex;
		flex-direction: column;
		flex: none;
	}
	header {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 12px 16px;
		border-bottom: 1px solid var(--cLn);
	}
	header .n {
		width: 20px;
		height: 20px;
		border-radius: 50%;
		background: var(--amb);
		color: var(--ink);
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 11px;
		font-weight: 800;
		font-family: var(--mono);
		box-shadow: 0 0 10px var(--ambGlow);
		flex: none;
	}
	header h2 {
		font-size: 20px;
		white-space: nowrap;
	}
	header .sub {
		font-size: 12px;
		color: var(--mut);
		white-space: nowrap;
	}
	header a {
		font-size: 12px;
		white-space: nowrap;
	}
	.spacer {
		flex: 1;
	}
	.row-ny {
		display: grid;
		grid-template-columns: 118px minmax(0, 1fr) 210px 64px auto;
		gap: 14px;
		align-items: center;
		padding: 10px 16px;
		border-top: 1px solid var(--cLn);
	}
	header + .row-ny {
		border-top: 0;
	}
	.row-ny:hover {
		background: var(--p1);
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
		color: var(--tx);
	}
	.what .sub {
		font-size: 11.5px;
		color: var(--mut);
	}
	.what .sub .mono {
		font-size: 11px;
	}
	.amb {
		color: var(--amb);
	}
	.ws {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 12px;
		min-width: 0;
	}
	.age {
		font-size: 11px;
		color: var(--mut);
		text-align: right;
	}
	.cta {
		padding: 4px 12px;
		border-radius: 9px;
	}
	@media (max-width: 900px) {
		.row-ny {
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
