<script lang="ts">
	// List or tree, for a pane that lists files (a run's Changes, a review's
	// files). One per-browser choice: switching here switches them all.
	import { prefs } from '$lib/app/prefs.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
</script>

<Segmented
	glass={false}
	size="sm"
	label={t('ui.fileLayout.label')}
	options={[
		{ value: 'list', label: t('ui.fileLayout.list') },
		{ value: 'tree', label: t('ui.fileLayout.tree') }
	]}
	value={prefs.fileLayout}
	onchange={(v) => prefs.setFileLayout(v)}
/>
