
// this file is generated — do not edit it


/// <reference types="@sveltejs/kit" />

/**
 * This module provides access to environment variables that are injected _statically_ into your bundle at build time and are limited to _private_ access.
 * 
 * |         | Runtime                                                                    | Build time                                                               |
 * | ------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
 * | Private | [`$env/dynamic/private`](https://svelte.dev/docs/kit/$env-dynamic-private) | [`$env/static/private`](https://svelte.dev/docs/kit/$env-static-private) |
 * | Public  | [`$env/dynamic/public`](https://svelte.dev/docs/kit/$env-dynamic-public)   | [`$env/static/public`](https://svelte.dev/docs/kit/$env-static-public)   |
 * 
 * Static environment variables are [loaded by Vite](https://vitejs.dev/guide/env-and-mode.html#env-files) from `.env` files and `process.env` at build time and then statically injected into your bundle at build time, enabling optimisations like dead code elimination.
 * 
 * **_Private_ access:**
 * 
 * - This module cannot be imported into client-side code
 * - This module only includes variables that _do not_ begin with [`config.kit.env.publicPrefix`](https://svelte.dev/docs/kit/configuration#env) _and do_ start with [`config.kit.env.privatePrefix`](https://svelte.dev/docs/kit/configuration#env) (if configured)
 * 
 * For example, given the following build time environment:
 * 
 * ```env
 * ENVIRONMENT=production
 * PUBLIC_BASE_URL=http://site.com
 * ```
 * 
 * With the default `publicPrefix` and `privatePrefix`:
 * 
 * ```ts
 * import { ENVIRONMENT, PUBLIC_BASE_URL } from '$env/static/private';
 * 
 * console.log(ENVIRONMENT); // => "production"
 * console.log(PUBLIC_BASE_URL); // => throws error during build
 * ```
 * 
 * The above values will be the same _even if_ different values for `ENVIRONMENT` or `PUBLIC_BASE_URL` are set at runtime, as they are statically replaced in your code with their build time values.
 */
declare module '$env/static/private' {
	export const DELTA_PAGER: string;
	export const PI_SESSION_FILE: string;
	export const npm_config_user_agent: string;
	export const AGENT: string;
	export const CI: string;
	export const GIT_EDITOR: string;
	export const SYSTEMD_PAGER: string;
	export const CARGO_TERM_PROGRESS_WHEN: string;
	export const GLAB_PAGER: string;
	export const OMP_AUTH_BROKER_TOKEN: string;
	export const TF_INPUT: string;
	export const npm_node_execpath: string;
	export const CLOUDSDK_CORE_DISABLE_PROMPTS: string;
	export const SHLVL: string;
	export const __PI_NATIVE_VARIANT_CACHE: string;
	export const npm_config_noproxy: string;
	export const HOME: string;
	export const LESS: string;
	export const OLDPWD: string;
	export const npm_package_json: string;
	export const PYTHONUNBUFFERED: string;
	export const YARN_ENABLE_TELEMETRY: string;
	export const npm_package_engines_node: string;
	export const GOTOOLCHAIN: string;
	export const PAGER: string;
	export const npm_config_userconfig: string;
	export const npm_config_local_prefix: string;
	export const NO_COLOR: string;
	export const PI_SESSION_ID: string;
	export const SSH_ASKPASS: string;
	export const VISUAL: string;
	export const npm_config_yes: string;
	export const npm_config_engine_strict: string;
	export const COLOR: string;
	export const npm_config_progress: string;
	export const npm_config_audit: string;
	export const APOTHECARY_DEV_DSN: string;
	export const PIP_DISABLE_PIP_VERSION_CHECK: string;
	export const _: string;
	export const npm_config_prefix: string;
	export const npm_config_npm_version: string;
	export const BAT_PAGER: string;
	export const YARN_ENABLE_PROGRESS_BARS: string;
	export const OMP_AUTH_BROKER_URL: string;
	export const TERM: string;
	export const npm_config_cache: string;
	export const COMPOSER_NO_INTERACTION: string;
	export const npm_config_node_gyp: string;
	export const PATH: string;
	export const AWS_PAGER: string;
	export const PI_NO_TITLE: string;
	export const NODE: string;
	export const npm_package_name: string;
	export const GH_PROMPT_DISABLED: string;
	export const OMP_SKIP_SETUP: string;
	export const GH_PAGER: string;
	export const LS_COLORS: string;
	export const MANPAGER: string;
	export const PNPM_DISABLE_SELF_UPDATE_CHECK: string;
	export const npm_config_fund: string;
	export const npm_lifecycle_script: string;
	export const DEBIAN_FRONTEND: string;
	export const GIT_TERMINAL_PROMPT: string;
	export const GOPATH: string;
	export const npm_package_version: string;
	export const npm_lifecycle_event: string;
	export const OMPCODE: string;
	export const npm_config_update_notifier: string;
	export const OMP_MCP_TIMEOUT_MS: string;
	export const CLAUDECODE: string;
	export const APOTHECARY_AGENT: string;
	export const HOMEBREW_PAGER: string;
	export const GIT_PAGER: string;
	export const npm_config_globalconfig: string;
	export const npm_config_init_module: string;
	export const GPG_TTY: string;
	export const MYSQL_PAGER: string;
	export const PWD: string;
	export const npm_execpath: string;
	export const PSQL_PAGER: string;
	export const TF_IN_AUTOMATION: string;
	export const npm_config_global_prefix: string;
	export const npm_package_engines_npm: string;
	export const npm_command: string;
	export const PNPM_UPDATE_NOTIFIER: string;
	export const GOLANG_VERSION: string;
	export const PIP_NO_INPUT: string;
	export const EDITOR: string;
	export const INIT_CWD: string;
	export const TEST: string;
	export const VITEST: string;
	export const NODE_ENV: string;
}

/**
 * This module provides access to environment variables that are injected _statically_ into your bundle at build time and are _publicly_ accessible.
 * 
 * |         | Runtime                                                                    | Build time                                                               |
 * | ------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
 * | Private | [`$env/dynamic/private`](https://svelte.dev/docs/kit/$env-dynamic-private) | [`$env/static/private`](https://svelte.dev/docs/kit/$env-static-private) |
 * | Public  | [`$env/dynamic/public`](https://svelte.dev/docs/kit/$env-dynamic-public)   | [`$env/static/public`](https://svelte.dev/docs/kit/$env-static-public)   |
 * 
 * Static environment variables are [loaded by Vite](https://vitejs.dev/guide/env-and-mode.html#env-files) from `.env` files and `process.env` at build time and then statically injected into your bundle at build time, enabling optimisations like dead code elimination.
 * 
 * **_Public_ access:**
 * 
 * - This module _can_ be imported into client-side code
 * - **Only** variables that begin with [`config.kit.env.publicPrefix`](https://svelte.dev/docs/kit/configuration#env) (which defaults to `PUBLIC_`) are included
 * 
 * For example, given the following build time environment:
 * 
 * ```env
 * ENVIRONMENT=production
 * PUBLIC_BASE_URL=http://site.com
 * ```
 * 
 * With the default `publicPrefix` and `privatePrefix`:
 * 
 * ```ts
 * import { ENVIRONMENT, PUBLIC_BASE_URL } from '$env/static/public';
 * 
 * console.log(ENVIRONMENT); // => throws error during build
 * console.log(PUBLIC_BASE_URL); // => "http://site.com"
 * ```
 * 
 * The above values will be the same _even if_ different values for `ENVIRONMENT` or `PUBLIC_BASE_URL` are set at runtime, as they are statically replaced in your code with their build time values.
 */
declare module '$env/static/public' {
	
}

/**
 * This module provides access to environment variables set _dynamically_ at runtime and that are limited to _private_ access.
 * 
 * |         | Runtime                                                                    | Build time                                                               |
 * | ------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
 * | Private | [`$env/dynamic/private`](https://svelte.dev/docs/kit/$env-dynamic-private) | [`$env/static/private`](https://svelte.dev/docs/kit/$env-static-private) |
 * | Public  | [`$env/dynamic/public`](https://svelte.dev/docs/kit/$env-dynamic-public)   | [`$env/static/public`](https://svelte.dev/docs/kit/$env-static-public)   |
 * 
 * Dynamic environment variables are defined by the platform you're running on. For example if you're using [`adapter-node`](https://github.com/sveltejs/kit/tree/main/packages/adapter-node) (or running [`vite preview`](https://svelte.dev/docs/kit/cli)), this is equivalent to `process.env`.
 * 
 * **_Private_ access:**
 * 
 * - This module cannot be imported into client-side code
 * - This module includes variables that _do not_ begin with [`config.kit.env.publicPrefix`](https://svelte.dev/docs/kit/configuration#env) _and do_ start with [`config.kit.env.privatePrefix`](https://svelte.dev/docs/kit/configuration#env) (if configured)
 * 
 * > [!NOTE] In `dev`, `$env/dynamic` includes environment variables from `.env`. In `prod`, this behavior will depend on your adapter.
 * 
 * > [!NOTE] To get correct types, environment variables referenced in your code should be declared (for example in an `.env` file), even if they don't have a value until the app is deployed:
 * >
 * > ```env
 * > MY_FEATURE_FLAG=
 * > ```
 * >
 * > You can override `.env` values from the command line like so:
 * >
 * > ```sh
 * > MY_FEATURE_FLAG="enabled" npm run dev
 * > ```
 * 
 * For example, given the following runtime environment:
 * 
 * ```env
 * ENVIRONMENT=production
 * PUBLIC_BASE_URL=http://site.com
 * ```
 * 
 * With the default `publicPrefix` and `privatePrefix`:
 * 
 * ```ts
 * import { env } from '$env/dynamic/private';
 * 
 * console.log(env.ENVIRONMENT); // => "production"
 * console.log(env.PUBLIC_BASE_URL); // => undefined
 * ```
 */
declare module '$env/dynamic/private' {
	export const env: {
		DELTA_PAGER: string;
		PI_SESSION_FILE: string;
		npm_config_user_agent: string;
		AGENT: string;
		CI: string;
		GIT_EDITOR: string;
		SYSTEMD_PAGER: string;
		CARGO_TERM_PROGRESS_WHEN: string;
		GLAB_PAGER: string;
		OMP_AUTH_BROKER_TOKEN: string;
		TF_INPUT: string;
		npm_node_execpath: string;
		CLOUDSDK_CORE_DISABLE_PROMPTS: string;
		SHLVL: string;
		__PI_NATIVE_VARIANT_CACHE: string;
		npm_config_noproxy: string;
		HOME: string;
		LESS: string;
		OLDPWD: string;
		npm_package_json: string;
		PYTHONUNBUFFERED: string;
		YARN_ENABLE_TELEMETRY: string;
		npm_package_engines_node: string;
		GOTOOLCHAIN: string;
		PAGER: string;
		npm_config_userconfig: string;
		npm_config_local_prefix: string;
		NO_COLOR: string;
		PI_SESSION_ID: string;
		SSH_ASKPASS: string;
		VISUAL: string;
		npm_config_yes: string;
		npm_config_engine_strict: string;
		COLOR: string;
		npm_config_progress: string;
		npm_config_audit: string;
		APOTHECARY_DEV_DSN: string;
		PIP_DISABLE_PIP_VERSION_CHECK: string;
		_: string;
		npm_config_prefix: string;
		npm_config_npm_version: string;
		BAT_PAGER: string;
		YARN_ENABLE_PROGRESS_BARS: string;
		OMP_AUTH_BROKER_URL: string;
		TERM: string;
		npm_config_cache: string;
		COMPOSER_NO_INTERACTION: string;
		npm_config_node_gyp: string;
		PATH: string;
		AWS_PAGER: string;
		PI_NO_TITLE: string;
		NODE: string;
		npm_package_name: string;
		GH_PROMPT_DISABLED: string;
		OMP_SKIP_SETUP: string;
		GH_PAGER: string;
		LS_COLORS: string;
		MANPAGER: string;
		PNPM_DISABLE_SELF_UPDATE_CHECK: string;
		npm_config_fund: string;
		npm_lifecycle_script: string;
		DEBIAN_FRONTEND: string;
		GIT_TERMINAL_PROMPT: string;
		GOPATH: string;
		npm_package_version: string;
		npm_lifecycle_event: string;
		OMPCODE: string;
		npm_config_update_notifier: string;
		OMP_MCP_TIMEOUT_MS: string;
		CLAUDECODE: string;
		APOTHECARY_AGENT: string;
		HOMEBREW_PAGER: string;
		GIT_PAGER: string;
		npm_config_globalconfig: string;
		npm_config_init_module: string;
		GPG_TTY: string;
		MYSQL_PAGER: string;
		PWD: string;
		npm_execpath: string;
		PSQL_PAGER: string;
		TF_IN_AUTOMATION: string;
		npm_config_global_prefix: string;
		npm_package_engines_npm: string;
		npm_command: string;
		PNPM_UPDATE_NOTIFIER: string;
		GOLANG_VERSION: string;
		PIP_NO_INPUT: string;
		EDITOR: string;
		INIT_CWD: string;
		TEST: string;
		VITEST: string;
		NODE_ENV: string;
		[key: `PUBLIC_${string}`]: undefined;
		[key: `${string}`]: string | undefined;
	}
}

/**
 * This module provides access to environment variables set _dynamically_ at runtime and that are _publicly_ accessible.
 * 
 * |         | Runtime                                                                    | Build time                                                               |
 * | ------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
 * | Private | [`$env/dynamic/private`](https://svelte.dev/docs/kit/$env-dynamic-private) | [`$env/static/private`](https://svelte.dev/docs/kit/$env-static-private) |
 * | Public  | [`$env/dynamic/public`](https://svelte.dev/docs/kit/$env-dynamic-public)   | [`$env/static/public`](https://svelte.dev/docs/kit/$env-static-public)   |
 * 
 * Dynamic environment variables are defined by the platform you're running on. For example if you're using [`adapter-node`](https://github.com/sveltejs/kit/tree/main/packages/adapter-node) (or running [`vite preview`](https://svelte.dev/docs/kit/cli)), this is equivalent to `process.env`.
 * 
 * **_Public_ access:**
 * 
 * - This module _can_ be imported into client-side code
 * - **Only** variables that begin with [`config.kit.env.publicPrefix`](https://svelte.dev/docs/kit/configuration#env) (which defaults to `PUBLIC_`) are included
 * 
 * > [!NOTE] In `dev`, `$env/dynamic` includes environment variables from `.env`. In `prod`, this behavior will depend on your adapter.
 * 
 * > [!NOTE] To get correct types, environment variables referenced in your code should be declared (for example in an `.env` file), even if they don't have a value until the app is deployed:
 * >
 * > ```env
 * > MY_FEATURE_FLAG=
 * > ```
 * >
 * > You can override `.env` values from the command line like so:
 * >
 * > ```sh
 * > MY_FEATURE_FLAG="enabled" npm run dev
 * > ```
 * 
 * For example, given the following runtime environment:
 * 
 * ```env
 * ENVIRONMENT=production
 * PUBLIC_BASE_URL=http://example.com
 * ```
 * 
 * With the default `publicPrefix` and `privatePrefix`:
 * 
 * ```ts
 * import { env } from '$env/dynamic/public';
 * console.log(env.ENVIRONMENT); // => undefined, not public
 * console.log(env.PUBLIC_BASE_URL); // => "http://example.com"
 * ```
 * 
 * ```
 * 
 * ```
 */
declare module '$env/dynamic/public' {
	export const env: {
		[key: `PUBLIC_${string}`]: string | undefined;
	}
}
