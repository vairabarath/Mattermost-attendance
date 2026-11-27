// Calls the plugin's server part, which forwards to the attendance bot.

import manifest from 'manifest';

import type {Options} from '@mattermost/types/client4';

import {Client4} from 'mattermost-redux/client';

let basePath = '';

// Keep only the path of the SiteURL so requests stay same-origin even when the
// browser reached the server through a different hostname.
export function setSiteURL(siteURL: string | undefined) {
    basePath = siteURL ? new URL(siteURL).pathname.replace(/\/$/, '') : '';
}

// publicURL is a file in the plugin's public/ folder, e.g. the app-bar icon.
export const publicURL = (file: string) => `${basePath}/plugins/${manifest.id}/public/${file}`;

// doFetch calls /plugins/<id>/api/v1<path>. Client4.getOptions adds
// X-Requested-With and, for non-GET requests, the session's CSRF token;
// without the token Mattermost forwards the request as unauthenticated.
export async function doFetch<T>(path: string, options: Options = {}): Promise<T> {
    const response = await fetch(
        `${basePath}/plugins/${manifest.id}/api/v1${path}`,
        Client4.getOptions(options),
    );
    const body = await response.json().catch(() => null);
    if (!response.ok) {
        // Field errors (e.g. the end-of-day form) come as {errors: {field: msg}}.
        const fieldErrors = body?.errors && Object.values(body.errors as Record<string, string>);
        const err = new Error(body?.error || fieldErrors?.[0] || `Request failed with status ${response.status}`) as Error & {body?: unknown};
        err.body = body;
        throw err;
    }
    return body as T;
}

export type Health = {ok: boolean; version: string; error?: string};

export const fetchHealth = () => doFetch<Health>('/health');
