
// this file is generated — do not edit it


declare module "svelte/elements" {
	export interface HTMLAttributes<T> {
		'data-sveltekit-keepfocus'?: true | '' | 'off' | undefined | null;
		'data-sveltekit-noscroll'?: true | '' | 'off' | undefined | null;
		'data-sveltekit-preload-code'?:
			| true
			| ''
			| 'eager'
			| 'viewport'
			| 'hover'
			| 'tap'
			| 'off'
			| undefined
			| null;
		'data-sveltekit-preload-data'?: true | '' | 'hover' | 'tap' | 'off' | undefined | null;
		'data-sveltekit-reload'?: true | '' | 'off' | undefined | null;
		'data-sveltekit-replacestate'?: true | '' | 'off' | undefined | null;
	}
}

export {};


declare module "$app/types" {
	type MatcherParam<M> = M extends (param : string) => param is (infer U extends string) ? U : string;

	export interface AppTypes {
		RouteId(): "/" | "/channels" | "/flows" | "/flows/[name]" | "/inbox" | "/integrations" | "/projects" | "/projects/new" | "/projects/[id]" | "/runs" | "/runs/[id]" | "/settings";
		RouteParams(): {
			"/flows/[name]": { name: string };
			"/projects/[id]": { id: string };
			"/runs/[id]": { id: string }
		};
		LayoutParams(): {
			"/": { name?: string | undefined; id?: string | undefined };
			"/channels": Record<string, never>;
			"/flows": { name?: string | undefined };
			"/flows/[name]": { name: string };
			"/inbox": Record<string, never>;
			"/integrations": Record<string, never>;
			"/projects": { id?: string | undefined };
			"/projects/new": Record<string, never>;
			"/projects/[id]": { id: string };
			"/runs": { id?: string | undefined };
			"/runs/[id]": { id: string };
			"/settings": Record<string, never>
		};
		Pathname(): "/" | "/channels" | "/flows" | `/flows/${string}` & {} | "/inbox" | "/integrations" | "/projects" | "/projects/new" | `/projects/${string}` & {} | `/runs/${string}` & {} | "/settings";
		ResolvedPathname(): `${"" | `/${string}`}${ReturnType<AppTypes['Pathname']>}`;
		Asset(): string & {};
	}
}