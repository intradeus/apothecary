<script lang="ts">
	// "shared by @ada · Operator" on a workshop someone else owns; nothing on
	// one's own.
	import type { Project } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import { roleLabel } from './MembersTab.svelte';

	let { project }: { project: Project } = $props();
</script>

{#if project.ownerHandle}
	<span class="pill sm tint-accent"
		>{t('workshop.members.sharedBy', {
			handle: project.ownerHandle,
			role: roleLabel(project.access.role)
		})}</span
	>
{/if}
