// The Attendance panel. Phase 0: the plugin loads and registers; the panel
// (app-bar icon + right-hand side) arrives in Phase 2 — see ui-plan.md.

// Compass tokens are declared in cascade layers, so this is safe to load next to the host's
// copy. Never import '@mattermost/compass-ui/styles/standalone' here: it overrides the user's theme.
import '@mattermost/compass-ui/styles';

import {setSiteURL} from 'client';
import manifest from 'manifest';
import type {Store} from 'redux';

import type {GlobalState} from '@mattermost/types/store';

import {getConfig} from 'mattermost-redux/selectors/entities/general';

import type {PluginRegistry} from 'types/mattermost-webapp';

export default class Plugin {
    public async initialize(_registry: PluginRegistry, store: Store<GlobalState>) {
        setSiteURL(getConfig(store.getState()).SiteURL);
    }
}

declare global {
    interface Window {
        registerPlugin(pluginId: string, plugin: Plugin): void;
    }
}

window.registerPlugin(manifest.id, new Plugin());
