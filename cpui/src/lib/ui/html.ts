// confirmDialog() bodies are trusted markup. Anything a user or the engine
// named (a workshop, an integration) goes through esc() first.
export const esc = (s: string) =>
	s.replace(
		/[&<>"']/g,
		(c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!
	);
